package config

import (
	"fmt"
	"io/ioutil"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	BindAddress       string `yaml:"bindAddress"`
	Port              string `yaml:"port"`
	Path              string `yaml:"path"`
	Db                string `yaml:"database"`
	DataDir           string `yaml:"dataDir"`
	SteamAPIKey       string `yaml:"steamAPIKey"`
	Flag              string `yaml:"flag"`
	WanIP             string `yaml:"wanip"`
	WhiteAdminIP      string `yaml:"whiteadminip"`
	Token             string `yaml:"token"`
	DstVersionUrl     string `yaml:"dstVersionUrl"`
	AutoUpdateModinfo struct {
		Enable              bool `yaml:"enable"`
		CheckInterval       int  `yaml:"checkInterval"`
		UpdateCheckInterval int  `yaml:"updateCheckInterval"`
	} `yaml:"autoUpdateModinfo"`
	// DailyVersionCheck 每日固定时间用 SteamCMD 做一次权威版本检查。
	DailyVersionCheck struct {
		Enable *bool  `yaml:"enable"`
		Time   string `yaml:"time"`
	} `yaml:"dailyVersionCheck"`
}

const (
	DefaultConfigPath = "./config.yml"
	DefaultPort       = "8082"
	DefaultDataDir    = "./"
)

var Cfg *Config

func Load() *Config {
	// 先初始化默认配置：config.yml 缺失或为空时也要能启动，而不是对 nil 解引用 panic。
	c := &Config{}
	yamlFile, err := ioutil.ReadFile(DefaultConfigPath)
	if err != nil {
		fmt.Println(err.Error())
	}
	if len(yamlFile) > 0 {
		if err = yaml.Unmarshal(yamlFile, c); err != nil {
			fmt.Println(err.Error())
			c = &Config{}
		}
	}
	if c.Port == "" {
		c.Port = DefaultPort
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if c.AutoUpdateModinfo.UpdateCheckInterval == 0 {
		c.AutoUpdateModinfo.UpdateCheckInterval = 10
	}
	if c.AutoUpdateModinfo.CheckInterval == 0 {
		c.AutoUpdateModinfo.CheckInterval = 5
	}
	if c.DstVersionUrl == "" {
		c.DstVersionUrl = "http://ver.tugos.cn/getLocalVersion"
	}
	if c.DailyVersionCheck.Time == "" {
		c.DailyVersionCheck.Time = "04:00"
	}
	Cfg = c
	return c
}

// GetDbPath 获取数据库文件的完整路径
func (c *Config) GetDbPath() string {
	return filepath.Join(c.DataDir, c.Db)
}
