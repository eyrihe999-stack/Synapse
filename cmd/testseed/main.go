// cmd/testseed 仅开发期用:给指定 user_id 签发 JWT + 写 session,print token 到 stdout。
//
// 跑法:go run ./cmd/testseed -user 2
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/eyrihe999-stack/Synapse/config"
	"github.com/eyrihe999-stack/Synapse/internal/user"
	usersvc "github.com/eyrihe999-stack/Synapse/internal/user/service"
	"github.com/eyrihe999-stack/Synapse/internal/common/database"
	"github.com/eyrihe999-stack/Synapse/internal/common/logger"
	"github.com/eyrihe999-stack/Synapse/internal/common/jwt"
)

func main() {
	var userID uint64
	var email string
	flag.Uint64Var(&userID, "user", 0, "target user_id")
	flag.StringVar(&email, "email", "", "target email (just for JWT claims, no DB check)")
	flag.Parse()
	if userID == 0 {
		fmt.Fprintln(os.Stderr, "need -user <id>")
		os.Exit(2)
	}

	cfg, err := config.Load()
	must(err)
	l, err := logger.GetLogger(&cfg.Log)
	must(err)

	rdb, err := database.NewRedis(&cfg.Redis)
	must(err)

	sessionStore := usersvc.NewSessionStore(rdb, l)

	jwtMgr := jwt.NewJWTManager(jwt.JWTConfig{
		SecretKey:            cfg.JWT.SecretKey,
		AccessTokenDuration:  cfg.JWT.AccessTokenDuration,
		RefreshTokenDuration: cfg.JWT.RefreshTokenDuration,
		Issuer:               cfg.JWT.Issuer,
	})

	deviceID := fmt.Sprintf("testseed-%d", time.Now().UnixNano())
	accessToken, _, err := jwtMgr.GenerateAccessToken(userID, email, deviceID)
	must(err)

	ttl := cfg.JWT.RefreshTokenDuration
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	err = sessionStore.Save(context.Background(), userID, deviceID, user.SessionInfo{
		JTI:            deviceID,
		DeviceName:     "testseed",
		LoginAt:        time.Now().Unix(),
		SessionStartAt: time.Now().Unix(),
	}, ttl)
	must(err)

	fmt.Println(accessToken)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
