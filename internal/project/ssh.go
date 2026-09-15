package project

import "github.com/pietervanleuven/rehost/internal/ssh"

// SSHConfig bridges a project host to the connection layer.
func (h Host) SSHConfig() ssh.Config {
	return ssh.Config{
		Host:    h.Host,
		Port:    h.Port,
		User:    h.User,
		Auth:    ssh.AuthMethod(h.Auth),
		KeyPath: h.KeyPath,
	}
}
