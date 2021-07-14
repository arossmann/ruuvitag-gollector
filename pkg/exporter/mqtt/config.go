package mqtt

import (
	"time"
)

type Config struct {
	Addr              string
	Topic			  string
	ClientId          string
	Username          string
	Password          string
	CaFile            string
	AutoReconnect     bool
	ReconnectInterval time.Duration
}
