package syslog

import (
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"

	"github.com/hashicorp/nomad/client/allocdir"
	"github.com/hashicorp/nomad/client/driver/logrotator"
	"github.com/hashicorp/nomad/nomad/structs"
	"github.com/mcuadros/go-syslog"
)

type LogCollectorContext struct {
	TaskName       string
	AllocDir       *allocdir.AllocDir
	LogConfig      *structs.LogConfig
	PortUpperBound uint
	PortLowerBound uint
}

type SyslogCollectorState struct {
	IsolationConfig *IsolationConfig
	Addr            string
}

type LogCollector interface {
	LaunchCollector(ctx *LogCollectorContext) (*SyslogCollectorState, error)
	Exit() error
	UpdateLogConfig(logConfig *structs.LogConfig) error
}

type IsolationConfig struct {
}

type SyslogCollector struct {
	addr      net.Addr
	logConfig *structs.LogConfig
	ctx       *LogCollectorContext

	lro     *logrotator.LogRotator
	lre     *logrotator.LogRotator
	server  *syslog.Server
	taskDir string

	logger *log.Logger
}

func NewSyslogCollector(logger *log.Logger) *SyslogCollector {
	return &SyslogCollector{logger: logger}
}

func (s *SyslogCollector) LaunchCollector(ctx *LogCollectorContext) (*SyslogCollectorState, error) {
	addr, err := s.getFreePort(ctx.PortLowerBound, ctx.PortUpperBound)
	if err != nil {
		return nil, err
	}
	s.logger.Printf("sylog-server: launching syslog server on addr: %v", addr)
	s.ctx = ctx
	// configuring the task dir
	if err := s.configureTaskDir(); err != nil {
		return nil, err
	}

	channel := make(syslog.LogPartsChannel)
	handler := syslog.NewChannelHandler(channel)

	s.server = syslog.NewServer()
	s.server.SetFormat(&CustomParser{logger: s.logger})
	s.server.SetHandler(handler)
	s.server.ListenTCP(addr.String())
	if err := s.server.Boot(); err != nil {
		return nil, err
	}
	r, w := io.Pipe()
	logFileSize := int64(ctx.LogConfig.MaxFileSizeMB * 1024 * 1024)
	lro, err := logrotator.NewLogRotator(filepath.Join(s.taskDir, allocdir.TaskLocal),
		fmt.Sprintf("%v.stdout", ctx.TaskName), ctx.LogConfig.MaxFiles,
		logFileSize, s.logger)
	if err != nil {
		return nil, err
	}
	go lro.Start(r)

	go func(channel syslog.LogPartsChannel) {
		for logParts := range channel {
			message := logParts["content"].(string)
			w.Write([]byte(message))
		}
	}(channel)
	go s.server.Wait()
	return &SyslogCollectorState{Addr: addr.String()}, nil
}

func (s *SyslogCollector) Exit() error {
	return nil
}

func (s *SyslogCollector) UpdateLogConfig(logConfig *structs.LogConfig) error {
	return nil
}

// configureTaskDir sets the task dir in the executor
func (s *SyslogCollector) configureTaskDir() error {
	taskDir, ok := s.ctx.AllocDir.TaskDirs[s.ctx.TaskName]
	if !ok {
		return fmt.Errorf("couldn't find task directory for task %v", s.ctx.TaskName)
	}
	s.taskDir = taskDir
	return nil
}

// getFreePort returns a free port ready to be listened on between upper and
// lower bounds
func (s *SyslogCollector) getFreePort(lowerBound uint, upperBound uint) (net.Addr, error) {
	for i := lowerBound; i <= upperBound; i++ {
		addr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("localhost:%v", i))
		if err != nil {
			return nil, err
		}
		l, err := net.ListenTCP("tcp", addr)
		if err != nil {
			continue
		}
		defer l.Close()
		return l.Addr(), nil
	}
	return nil, fmt.Errorf("No free port found")
}
