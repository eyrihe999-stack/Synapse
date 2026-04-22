package sls

import (
	"fmt"
	"time"

	sls "github.com/aliyun/aliyun-log-go-sdk"
	"github.com/gogo/protobuf/proto"
	"github.com/sirupsen/logrus"
)

// entryToSLSLog converts a logrus entry to an SLS log.
func entryToSLSLog(entry *logrus.Entry, metadata map[string]string) *sls.Log {
	log := &sls.Log{
		Time: proto.Uint32(uint32(entry.Time.Unix())),
	}

	contents := make([]*sls.LogContent, 0)

	contents = append(contents,
		&sls.LogContent{
			Key:   proto.String("level"),
			Value: proto.String(entry.Level.String()),
		},
		&sls.LogContent{
			Key:   proto.String("message"),
			Value: proto.String(entry.Message),
		},
		&sls.LogContent{
			Key:   proto.String("time"),
			Value: proto.String(entry.Time.Format(time.RFC3339Nano)),
		},
	)

	if entry.HasCaller() {
		contents = append(contents,
			&sls.LogContent{
				Key:   proto.String("caller"),
				Value: proto.String(fmt.Sprintf("%s:%d", entry.Caller.File, entry.Caller.Line)),
			},
			&sls.LogContent{
				Key:   proto.String("function"),
				Value: proto.String(entry.Caller.Function),
			},
		)
	}

	for key, value := range entry.Data {
		if key == "error" || key == "err" {
			continue
		}
		contents = append(contents, &sls.LogContent{
			Key:   proto.String(key),
			Value: proto.String(fmt.Sprintf("%v", value)),
		})
	}

	if errMsg, stackTrace, hasError := extractError(entry.Data); hasError {
		contents = append(contents, &sls.LogContent{
			Key:   proto.String("error"),
			Value: proto.String(errMsg),
		})
		if stackTrace != "" {
			contents = append(contents, &sls.LogContent{
				Key:   proto.String("stack_trace"),
				Value: proto.String(stackTrace),
			})
		}
	}

	for key, value := range metadata {
		contents = append(contents, &sls.LogContent{
			Key:   proto.String("meta_" + key),
			Value: proto.String(value),
		})
	}

	log.Contents = contents
	return log
}

// extractError pulls error / stack_trace out of logrus fields.
func extractError(fields logrus.Fields) (errMsg string, stackTrace string, hasError bool) {
	if err, ok := fields["error"]; ok {
		if e, isError := err.(error); isError {
			errMsg = e.Error()
			hasError = true
		} else {
			errMsg = fmt.Sprintf("%v", err)
			hasError = true
		}
	} else if err, ok := fields["err"]; ok {
		if e, isError := err.(error); isError {
			errMsg = e.Error()
			hasError = true
		} else {
			errMsg = fmt.Sprintf("%v", err)
			hasError = true
		}
	}

	if stack, ok := fields["stack"]; ok {
		stackTrace = fmt.Sprintf("%v", stack)
	} else if stack, ok := fields["stacktrace"]; ok {
		stackTrace = fmt.Sprintf("%v", stack)
	}

	return errMsg, stackTrace, hasError
}
