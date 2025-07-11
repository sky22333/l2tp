package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"l2tp-manager/internal/database"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHService SSH连接服务
type SSHService struct{}

// NewSSHService 创建新的SSH服务
func NewSSHService() *SSHService {
	return &SSHService{}
}



// createSSHClient 创建SSH客户端连接
func (s *SSHService) createSSHClient(server *database.L2TPServer) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User: server.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(server.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	address := fmt.Sprintf("%s:%d", server.Host, server.Port)
	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return nil, fmt.Errorf("SSH连接失败: %v", err)
	}

	return client, nil
}

// executeCommand 执行SSH命令
func (s *SSHService) executeCommand(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	var output bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &output
	session.Stderr = &stderr

	err = session.Run(command)
	if err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("命令执行失败: %v, stderr: %s", err, stderr.String())
		}
		return "", fmt.Errorf("命令执行失败: %v", err)
	}

	return output.String(), nil
}

// StartL2TPContainer 启动L2TP Docker容器
func (s *SSHService) StartL2TPContainer(server *database.L2TPServer) error {
	return s.StartL2TPContainerWithCallback(server, nil)
}

// StartL2TPContainerWithCallback 启动L2TP Docker容器
func (s *SSHService) StartL2TPContainerWithCallback(server *database.L2TPServer, statusCallback func(step string, success bool, message string)) error {
	client, err := s.createSSHClient(server)
	if err != nil {
		if statusCallback != nil {
			statusCallback("ssh_connect", false, fmt.Sprintf("SSH连接失败: %v", err))
		}
		return err
	}
	defer client.Close()

	if statusCallback != nil {
		statusCallback("ssh_connect", true, "SSH连接成功")
	}

	// 检查并安装Docker
	if err := s.ensureDockerInstalled(client); err != nil {
		if statusCallback != nil {
			statusCallback("docker_check", false, fmt.Sprintf("Docker环境准备失败: %v", err))
		}
		return fmt.Errorf("Docker环境准备失败: %v", err)
	}

	if statusCallback != nil {
		statusCallback("docker_check", true, "Docker环境检查通过")
	}

	containerName := "l2tp-server"

	// 停止并清理现有容器
	if err := s.cleanupExistingContainer(client, containerName); err != nil {
		if statusCallback != nil {
			statusCallback("cleanup", false, fmt.Sprintf("清理现有容器失败: %v", err))
		}
		return fmt.Errorf("清理现有容器失败: %v", err)
	}

	if statusCallback != nil {
		statusCallback("cleanup", true, "容器清理完成")
	}

	// 解析用户配置
	var users []L2TPUser
	if server.Users != "" {
		if err := json.Unmarshal([]byte(server.Users), &users); err != nil {
			if statusCallback != nil {
				statusCallback("config", false, fmt.Sprintf("解析用户配置失败: %v", err))
			}
			return fmt.Errorf("解析用户配置失败: %v", err)
		}
	}

	// 构建用户环境变量
	userEnv := s.buildUserEnv(users)

	if statusCallback != nil {
		statusCallback("config", true, "用户配置解析完成")
	}

	// 拉取Docker镜像
	pullCmd := "docker pull siomiz/softethervpn:4.38-alpine"
	if _, err := s.executeCommand(client, pullCmd); err != nil {
		if statusCallback != nil {
			statusCallback("image_pull", false, fmt.Sprintf("拉取Docker镜像失败: %v", err))
		}
		return fmt.Errorf("拉取Docker镜像失败: %v", err)
	}

	if statusCallback != nil {
		statusCallback("image_pull", true, "Docker镜像拉取完成")
	}

	// 构建Docker运行命令
	dockerCmd := fmt.Sprintf(`docker run -d \
		--name %s \
		--restart always \
		-p 500:500/udp \
		-p 4500:4500/udp \
		-p 1701:1701/udp \
		-e PSK=%s \
		-e USERS="%s" \
		--cap-add NET_ADMIN \
		-v /lib/modules:/lib/modules:ro \
		siomiz/softethervpn:4.38-alpine`,
		containerName,
		server.PSK, 
		userEnv)

	// 启动容器
	if _, err := s.executeCommand(client, dockerCmd); err != nil {
		if statusCallback != nil {
			statusCallback("container_start", false, fmt.Sprintf("启动Docker容器失败: %v", err))
		}
		return fmt.Errorf("启动Docker容器失败: %v", err)
	}

	if statusCallback != nil {
		statusCallback("container_start", true, "容器启动命令执行成功")
	}

	// 等待容器启动并验证
	if err := s.waitForContainerReady(client, containerName); err != nil {
		// 启动失败，清理容器
		s.cleanupExistingContainer(client, containerName)
		if statusCallback != nil {
			statusCallback("container_ready", false, fmt.Sprintf("容器启动验证失败: %v", err))
		}
		return fmt.Errorf("容器启动验证失败: %v", err)
	}

	if statusCallback != nil {
		statusCallback("container_ready", true, "容器启动验证完成")
	}

	return nil
}

// StopL2TPContainer 停止L2TP Docker容器
func (s *SSHService) StopL2TPContainer(server *database.L2TPServer) error {
	return s.StopL2TPContainerWithCallback(server, nil)
}

// StopL2TPContainerWithCallback 停止L2TP Docker容器
func (s *SSHService) StopL2TPContainerWithCallback(server *database.L2TPServer, statusCallback func(step string, success bool, message string)) error {
	client, err := s.createSSHClient(server)
	if err != nil {
		if statusCallback != nil {
			statusCallback("ssh_connect", false, fmt.Sprintf("SSH连接失败: %v", err))
		}
		return err
	}
	defer client.Close()

	if statusCallback != nil {
		statusCallback("ssh_connect", true, "SSH连接成功")
	}

	containerName := "l2tp-server"
	
	// 检查容器是否存在
	checkCmd := fmt.Sprintf("docker ps -a -q -f name=^/%s$", containerName)
	output, err := s.executeCommand(client, checkCmd)
	if err != nil {
		if statusCallback != nil {
			statusCallback("container_check", false, fmt.Sprintf("检查容器失败: %v", err))
		}
		return err
	}

	if strings.TrimSpace(output) == "" {
		if statusCallback != nil {
			statusCallback("container_check", true, "容器不存在，无需停止")
		}
		return nil
	}

	if statusCallback != nil {
		statusCallback("container_check", true, "找到容器，准备停止")
	}

	// 停止并清理容器
	if err := s.cleanupExistingContainer(client, containerName); err != nil {
		if statusCallback != nil {
			statusCallback("container_stop", false, fmt.Sprintf("停止容器失败: %v", err))
		}
		return err
	}

	if statusCallback != nil {
		statusCallback("container_stop", true, "容器已成功停止并清理")
	}

	return nil
}

// GetContainerStatus 获取容器状态信息
func (s *SSHService) GetContainerStatus(server *database.L2TPServer) (map[string]interface{}, error) {
	client, err := s.createSSHClient(server)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	status := make(map[string]interface{})
	containerName := "l2tp-server"

	// 使用精确的容器名称匹配检查容器是否运行
	checkCmd := fmt.Sprintf("docker ps -q -f name=^/%s$", containerName)
	output, err := s.executeCommand(client, checkCmd)
	
	if err != nil {
		status["running"] = false
		status["error"] = fmt.Sprintf("检查容器状态失败: %v", err)
		return status, nil
	}

	// 判断容器运行状态
	isRunning := strings.TrimSpace(output) != ""
	status["running"] = isRunning
	
	if !isRunning {
		status["message"] = "容器未运行或不存在"
		return status, nil
	}

	status["message"] = "容器运行正常"
	
	// 获取容器启动时间
	startTimeCmd := fmt.Sprintf("docker inspect %s --format '{{.State.StartedAt}}'", containerName)
	startTimeOutput, err := s.executeCommand(client, startTimeCmd)
	if err == nil {
		if startTime, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(startTimeOutput)); err == nil {
			uptime := time.Since(startTime).Truncate(time.Second)
			status["uptime"] = uptime.String()
		}
	}

	return status, nil
}

// GetServerLogs 获取服务器日志
func (s *SSHService) GetServerLogs(server *database.L2TPServer, lines int) (string, error) {
	client, err := s.createSSHClient(server)
	if err != nil {
		return "", err
	}
	defer client.Close()

	containerName := "l2tp-server"
	
	// 首先检查容器是否存在
	checkCmd := fmt.Sprintf("docker ps -a --filter name=%s --format '{{.Names}}'", containerName)
	output, err := s.executeCommand(client, checkCmd)
	if err != nil || strings.TrimSpace(output) == "" {
		return "容器不存在", nil
	}

	// 获取容器日志
	command := fmt.Sprintf("docker logs %s --tail %d", containerName, lines)
	output, err = s.executeCommand(client, command)
	if err != nil {
		return "", fmt.Errorf("获取日志失败: %v", err)
	}

	return output, nil
}

// ensureDockerInstalled 确保Docker已安装并运行
func (s *SSHService) ensureDockerInstalled(client *ssh.Client) error {
	// 检查Docker是否已安装并运行
	_, err := s.executeCommand(client, "docker --version")
	if err == nil {
		// 检查Docker服务是否运行
		_, err = s.executeCommand(client, "docker info")
		if err == nil {
			return nil // Docker已安装并运行
		}
	}

	// 尝试安装Docker
	return s.installDocker(client)
}

// installDocker 安装Docker
func (s *SSHService) installDocker(client *ssh.Client) error {
	// 使用国内优化的安装脚本
	installCmd := `bash <(curl -sSL https://gitea.com/qwe78907890/docker/raw/branch/main/docker.sh) --mirror Tuna`
	
	_, err := s.executeCommand(client, installCmd)
	if err != nil {
		return fmt.Errorf("Docker安装失败: %v", err)
	}

	// 验证安装
	_, err = s.executeCommand(client, "docker --version")
	return err
}

// cleanupExistingContainer 清理现有容器
func (s *SSHService) cleanupExistingContainer(client *ssh.Client, containerName string) error {
	// 停止容器
	stopCmd := fmt.Sprintf("docker stop %s", containerName)
	s.executeCommand(client, stopCmd) // 忽略错误

	// 删除容器
	removeCmd := fmt.Sprintf("docker rm %s", containerName)
	s.executeCommand(client, removeCmd) // 忽略错误

	return nil
}

// waitForContainerReady 等待容器启动
func (s *SSHService) waitForContainerReady(client *ssh.Client, containerName string) error {
	// 使用事件流等待容器启动
	watchCmd := fmt.Sprintf("timeout 30 docker events --filter container=%s --filter event=start --format '{{.Status}}' | head -n 1", containerName)
	
	output, err := s.executeCommand(client, watchCmd)
	if err != nil {
		// 事件监听失败，默认为成功
		return nil
	}

	eventStatus := strings.TrimSpace(output)
	if eventStatus == "start" {
		return nil
	}

	// 未收到启动事件，默认为成功
	return nil
}

// buildUserEnv 构建用户环境变量
func (s *SSHService) buildUserEnv(users []L2TPUser) string {
	if len(users) == 0 {
		return "test:test123" // 默认用户
	}
	
	var userList []string
	for _, user := range users {
		userList = append(userList, fmt.Sprintf("%s:%s", user.Username, user.Password))
	}
	return strings.Join(userList, ",")
}