/*
 * Copyright 2023 Greptime Team
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package components

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/GreptimeTeam/gtctl/pkg/config"
	"github.com/GreptimeTeam/gtctl/pkg/logger"
	fileutils "github.com/GreptimeTeam/gtctl/pkg/utils/file"
)

type metaSrv struct {
	config *config.MetaSrv

	workingDirs WorkingDirs
	wg          *sync.WaitGroup
	logger      logger.Logger

	allocatedDirs
}

// 创建一个ClusterComponent接口
func NewMetaSrv(config *config.MetaSrv, workingDirs WorkingDirs,
	wg *sync.WaitGroup, logger logger.Logger) ClusterComponent {
	return &metaSrv{
		config:      config,
		workingDirs: workingDirs,
		wg:          wg,
		logger:      logger,
	}
}

func (m *metaSrv) Name() string {
	return "metasrv"
}

// 配置初始值
func (m *metaSrv) Start(ctx context.Context, stop context.CancelFunc, binary string) error {
	// Default bind address for meta srv.服务器接受客户端请求的网络地址，如果yaml文件没有设置的话，就使用127.0.0.1:3002,反之使用yaml文件
	bindAddr := net.JoinHostPort("127.0.0.1", "3002")
	if len(m.config.BindAddr) > 0 {
		bindAddr = m.config.BindAddr
	}

	for i := 0; i < m.config.Replicas; i++ {
		dirName := fmt.Sprintf("%s.%d", m.Name(), i)

		metaSrvLogDir := path.Join(m.workingDirs.LogsDir, dirName)
		if err := fileutils.EnsureDir(metaSrvLogDir); err != nil {
			return err
		}
		m.logsDirs = append(m.logsDirs, metaSrvLogDir)

		metaSrvPidDir := path.Join(m.workingDirs.PidsDir, dirName)
		if err := fileutils.EnsureDir(metaSrvPidDir); err != nil {
			return err
		}
		m.pidsDirs = append(m.pidsDirs, metaSrvPidDir)

		option := &RunOptions{
			Binary: binary,
			Name:   dirName,
			logDir: metaSrvLogDir,
			pidDir: metaSrvPidDir,
			args:   m.BuildArgs(i, bindAddr),
		}
		if err := runBinary(ctx, stop, option, m.wg, m.logger); err != nil {
			return err
		}
	}

	// Checking component running status with intervals.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

CHECKER:
	for {
		select {
		case <-ticker.C:
			if m.IsRunning(ctx) {
				break CHECKER
			}
		case <-ctx.Done():
			return fmt.Errorf("status checking failed: %v", ctx.Err())
		}
	}

	return nil
}

// 启动Meta服务的命令行参数，返回args
func (m *metaSrv) BuildArgs(params ...interface{}) []string {
	//配置日志级别
	logLevel := m.config.LogLevel //yaml文件中定义的日志级别
	if logLevel == "" {
		logLevel = DefaultLogLevel //info级别，一般的信息性消息，用于记录程序的正常运行状态
	}

	nodeID_, bindAddr_ := params[0], params[1] //赋予了两个参数空接口
	nodeID := nodeID_.(int)                    //类型断言，这时候nodeID存储的是int类型
	bindAddr := bindAddr_.(string)

	args := []string{
		fmt.Sprintf("--log-level=%s", logLevel),
		m.Name(), "start",
		fmt.Sprintf("--store-addr=%s", m.config.StoreAddr),
		fmt.Sprintf("--server-addr=%s", m.config.ServerAddr),
	}
	args = GenerateAddrArg("--http-addr", m.config.HTTPAddr, nodeID, args) //这里调用了上一个args

	args = GenerateAddrArg("--bind-addr", bindAddr, nodeID, args)

	if len(m.config.Config) > 0 {
		args = append(args, fmt.Sprintf("-c=%s", m.config.Config)) //这里最终形成了一个args，这是一个字符串数组
	}

	return args //将args给返回
}

// 检查这个服务是否还在运行
func (m *metaSrv) IsRunning(_ context.Context) bool {
	for i := 0; i < m.config.Replicas; i++ {
		addr := FormatAddrArg(m.config.HTTPAddr, i)
		_, httpPort, err := net.SplitHostPort(addr)
		if err != nil {
			m.logger.V(5).Infof("failed to split host port in %s: %s", m.Name(), err)
			return false
		}

		rsp, err := http.Get(fmt.Sprintf("http://localhost:%s/health", httpPort))
		if err != nil {
			m.logger.V(5).Infof("failed to get %s health: %s", m.Name(), err)
			return false
		}

		if rsp.StatusCode != http.StatusOK {
			return false
		}

		if err = rsp.Body.Close(); err != nil {
			return false
		}
	}

	return true
}
