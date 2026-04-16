package utils

import (
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	timestampBits     = uint8(41)
	datacenterIDBits  = uint8(5)
	workerIDBits      = uint8(5)
	sequenceBits      = uint8(12)

	maxDatacenterID = int64(-1) ^ (int64(-1) << datacenterIDBits)
	maxWorkerID     = int64(-1) ^ (int64(-1) << workerIDBits)
	maxSequence     = int64(-1) ^ (int64(-1) << sequenceBits)

	workerIDShift      = sequenceBits
	datacenterIDShift  = sequenceBits + workerIDBits
	timestampLeftShift = sequenceBits + workerIDBits + datacenterIDBits

	epoch int64 = 1704067200000 // 2024-01-01 00:00:00 UTC
)

var (
	ErrInvalidDatacenterID = errors.New("datacenter ID must be between 0 and 31")
	ErrInvalidWorkerID     = errors.New("worker ID must be between 0 and 31")
	ErrClockBackwards      = errors.New("clock moved backwards, refusing to generate ID")
	ErrSequenceOverflow    = errors.New("sequence overflow in the same millisecond")
)

type Snowflake struct {
	mu            sync.Mutex
	datacenterID  int64
	workerID      int64
	sequence      int64
	lastTimestamp int64
}

type SnowflakeConfig struct {
	DatacenterID int64
	WorkerID     int64
}

func NewSnowflake(config SnowflakeConfig) (*Snowflake, error) {
	datacenterID := config.DatacenterID
	workerID := config.WorkerID

	if datacenterID < -1 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidDatacenterID, datacenterID)
	}
	if workerID < -1 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidWorkerID, workerID)
	}

	if datacenterID == -1 || datacenterID == 0 {
		autoDatacenterID, autoWorkerID := autoDetectIDs()
		datacenterID = autoDatacenterID
		if workerID == -1 || workerID == 0 {
			workerID = autoWorkerID
		}
	} else if workerID == -1 || workerID == 0 {
		_, autoWorkerID := autoDetectIDs()
		workerID = autoWorkerID
	}

	if datacenterID > maxDatacenterID {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidDatacenterID, datacenterID)
	}
	if workerID > maxWorkerID {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidWorkerID, workerID)
	}

	return &Snowflake{
		datacenterID:  datacenterID,
		workerID:      workerID,
		sequence:      0,
		lastTimestamp: -1,
	}, nil
}

func autoDetectIDs() (datacenterID, workerID int64) {
	if dcID := os.Getenv("SNOWFLAKE_DATACENTER_ID"); dcID != "" {
		if id, err := strconv.ParseInt(dcID, 10, 64); err == nil && id >= 0 && id <= maxDatacenterID {
			datacenterID = id
		}
	}
	if wID := os.Getenv("SNOWFLAKE_WORKER_ID"); wID != "" {
		if id, err := strconv.ParseInt(wID, 10, 64); err == nil && id >= 0 && id <= maxWorkerID {
			workerID = id
			return
		}
	}

	if datacenterID == 0 {
		datacenterID = detectDatacenterIDFromK8s()
	}
	if workerID == 0 {
		workerID = detectWorkerIDFromK8s()
	}

	if datacenterID == 0 || workerID == 0 {
		ipDatacenterID, ipWorkerID := generateIDsFromIP()
		if datacenterID == 0 {
			datacenterID = ipDatacenterID
		}
		if workerID == 0 {
			workerID = ipWorkerID
		}
	}

	datacenterID = datacenterID & maxDatacenterID
	workerID = workerID & maxWorkerID
	return
}

func detectDatacenterIDFromK8s() int64 {
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = os.Getenv("NAMESPACE")
	}
	if namespace == "" {
		namespace = "default"
	}
	hash := md5.Sum([]byte(namespace))
	return int64(binary.BigEndian.Uint32(hash[:4])) & maxDatacenterID
}

func detectWorkerIDFromK8s() int64 {
	hostname := os.Getenv("HOSTNAME")
	if hostname == "" {
		if h, err := os.Hostname(); err == nil {
			hostname = h
		} else {
			return 0
		}
	}
	parts := strings.Split(hostname, "-")
	if len(parts) > 0 {
		lastPart := parts[len(parts)-1]
		if ordinal, err := strconv.ParseInt(lastPart, 10, 64); err == nil {
			return ordinal & maxWorkerID
		}
	}
	hash := md5.Sum([]byte(hostname))
	return int64(binary.BigEndian.Uint32(hash[:4])) & maxWorkerID
}

func generateIDsFromIP() (datacenterID, workerID int64) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return 1, 1
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipv4 := ipnet.IP.To4(); ipv4 != nil {
				datacenterID = int64(ipv4[2]) & maxDatacenterID
				workerID = int64(ipv4[3]) & maxWorkerID
				return
			}
		}
	}
	return generateIDsFromMAC()
}

func generateIDsFromMAC() (datacenterID, workerID int64) {
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		return 1, 1
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) >= 6 {
			mac := iface.HardwareAddr
			datacenterID = int64(mac[4]) & maxDatacenterID
			workerID = int64(mac[5]) & maxWorkerID
			return
		}
	}
	return 1, 1
}

func (s *Snowflake) NextID() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	timestamp := s.getTimestamp()
	if timestamp < s.lastTimestamp {
		return 0, fmt.Errorf("%w: last=%d, current=%d", ErrClockBackwards, s.lastTimestamp, timestamp)
	}

	if timestamp == s.lastTimestamp {
		s.sequence = (s.sequence + 1) & maxSequence
		if s.sequence == 0 {
			timestamp = s.waitNextMillis(s.lastTimestamp)
		}
	} else {
		s.sequence = 0
	}

	s.lastTimestamp = timestamp

	id := ((timestamp - epoch) << timestampLeftShift) |
		(s.datacenterID << datacenterIDShift) |
		(s.workerID << workerIDShift) |
		s.sequence

	return id, nil
}

func (s *Snowflake) NextIDs(count int) ([]int64, error) {
	if count <= 0 {
		return []int64{}, nil
	}
	ids := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		id, err := s.NextID()
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Snowflake) ParseID(id int64) (timestamp time.Time, datacenterID, workerID, sequence int64) {
	sequence = id & maxSequence
	workerID = (id >> workerIDShift) & maxWorkerID
	datacenterID = (id >> datacenterIDShift) & maxDatacenterID
	timestampMillis := (id >> timestampLeftShift) + epoch
	timestamp = time.Unix(timestampMillis/1000, (timestampMillis%1000)*1000000)
	return
}

func (s *Snowflake) getTimestamp() int64 {
	return time.Now().UnixNano() / 1e6
}

func (s *Snowflake) waitNextMillis(lastTimestamp int64) int64 {
	timestamp := s.getTimestamp()
	for timestamp <= lastTimestamp {
		timestamp = s.getTimestamp()
	}
	return timestamp
}

func (s *Snowflake) GetInfo() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]interface{}{
		"datacenter_id":  s.datacenterID,
		"worker_id":      s.workerID,
		"last_timestamp": s.lastTimestamp,
		"sequence":       s.sequence,
		"epoch":          epoch,
		"max_ids_per_ms": maxSequence + 1,
	}
}

var (
	globalSnowflake *Snowflake
	snowflakeMutex  sync.RWMutex
)

func InitSnowflake(config SnowflakeConfig) error {
	snowflakeMutex.Lock()
	defer snowflakeMutex.Unlock()
	sf, err := NewSnowflake(config)
	if err != nil {
		return err
	}
	globalSnowflake = sf
	return nil
}

func GetSnowflake() *Snowflake {
	snowflakeMutex.RLock()
	defer snowflakeMutex.RUnlock()
	return globalSnowflake
}

func GenerateID() (int64, error) {
	sf := GetSnowflake()
	if sf == nil {
		return 0, errors.New("snowflake generator not initialized")
	}
	return sf.NextID()
}

func GenerateIDs(count int) ([]int64, error) {
	sf := GetSnowflake()
	if sf == nil {
		return nil, errors.New("snowflake generator not initialized")
	}
	return sf.NextIDs(count)
}
