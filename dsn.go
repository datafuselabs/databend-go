package godatabend

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultClientTimeout  = 900 * time.Second // Timeout for network round trip + read out http response
	defaultLoginTimeout   = 60 * time.Second  // Timeout for retry for login EXCLUDING clientTimeout
	defaultRequestTimeout = 0 * time.Second   // Timeout for retry for request EXCLUDING clientTimeout
	defaultDomain         = "app.databend.com"
	defaultScheme         = "databend"

	SSL_MODE_DISABLE = "disable"
)

// Config is a set of configuration parameters
type Config struct {
	Tenant    string // Tenant
	Warehouse string // Warehouse
	User      string // Username
	Password  string // Password (requires User)
	Database  string // Database name

	AccessToken string

	Host            string
	Timeout         time.Duration
	IdleTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	Location        *time.Location
	Debug           bool
	UseDBLocation   bool
	GzipCompression bool
	Params          map[string]string
	TLSConfig       string
	SSLMode         string

	PresignedURLDisabled bool
}

// NewConfig creates a new config with default values
func NewConfig() *Config {
	return &Config{
		Host:        fmt.Sprintf("%s:443", defaultDomain),
		IdleTimeout: time.Hour,
		Location:    time.UTC,
		Params:      make(map[string]string),
	}
}

// FormatDSN formats the given Config into a DSN string which can be passed to
// the driver.
func (cfg *Config) FormatDSN() string {
	u := &url.URL{
		Host:   cfg.Host,
		Scheme: defaultScheme,
		Path:   "/",
	}
	if len(cfg.User) > 0 {
		if len(cfg.Password) > 0 {
			u.User = url.UserPassword(cfg.User, cfg.Password)
		} else {
			u.User = url.User(cfg.User)
		}
	}
	if len(cfg.Database) > 0 {
		u.Path = cfg.Database
	}
	query := u.Query()
	if cfg.Tenant != "" {
		query.Set("tenant", cfg.Tenant)
	}
	if cfg.Warehouse != "" {
		query.Set("warehouse", cfg.Warehouse)
	}
	if cfg.AccessToken != "" {
		query.Set("access_token", cfg.AccessToken)
	}
	if cfg.Timeout != 0 {
		query.Set("timeout", cfg.Timeout.String())
	}
	if cfg.IdleTimeout != 0 {
		query.Set("idle_timeout", cfg.IdleTimeout.String())
	}
	if cfg.ReadTimeout != 0 {
		query.Set("read_timeout", cfg.ReadTimeout.String())
	}
	if cfg.WriteTimeout != 0 {
		query.Set("write_timeout", cfg.WriteTimeout.String())
	}
	if cfg.Location != time.UTC && cfg.Location != nil {
		query.Set("location", cfg.Location.String())
	}
	if cfg.GzipCompression {
		query.Set("enable_http_compression", "1")
	}
	if cfg.Debug {
		query.Set("debug", "1")
	}
	if cfg.TLSConfig != "" {
		query.Set("tls_config", cfg.TLSConfig)
	}
	if cfg.SSLMode != "" {
		query.Set("sslmode", cfg.SSLMode)
	}
	if cfg.PresignedURLDisabled {
		query.Set("presigned_url_disabled", "1")
	}

	u.RawQuery = query.Encode()
	return u.String()
}

func (cfg *Config) url(extra map[string]string) *url.URL {
	u := &url.URL{
		Host: cfg.Host,
		Path: "/",
	}
	if len(cfg.User) > 0 {
		if len(cfg.Password) > 0 {
			u.User = url.UserPassword(cfg.User, cfg.Password)
		} else {
			u.User = url.User(cfg.User)
		}
	}
	query := u.Query()
	if len(cfg.Database) > 0 {
		query.Set("database", cfg.Database)
	}

	if cfg.SSLMode == SSL_MODE_DISABLE {
		u.Scheme = "http"
	} else {
		u.Scheme = "https"
	}

	for k, v := range cfg.Params {
		query.Set(k, v)
	}
	for k, v := range extra {
		query.Set(k, v)
	}

	u.RawQuery = query.Encode()
	return u
}

func (cfg *Config) AddParams(params map[string][]string) (err error) {
	for k, v := range params {
		if len(v) == 0 {
			continue
		}

		switch k {
		case "timeout":
			cfg.Timeout, err = time.ParseDuration(v[0])
		case "idle_timeout":
			cfg.IdleTimeout, err = time.ParseDuration(v[0])
		case "read_timeout":
			cfg.ReadTimeout, err = time.ParseDuration(v[0])
		case "write_timeout":
			cfg.WriteTimeout, err = time.ParseDuration(v[0])
		case "location":
			cfg.Location, err = time.LoadLocation(v[0])
		case "debug":
			cfg.Debug, err = strconv.ParseBool(v[0])
		case "default_format", "query", "database":
			err = fmt.Errorf("unknown option '%s'", k)
		case "enable_http_compression":
			cfg.GzipCompression, err = strconv.ParseBool(v[0])
			cfg.Params[k] = v[0]
		case "presigned_url_disabled":
			cfg.PresignedURLDisabled, err = strconv.ParseBool(v[0])
		case "tls_config":
			cfg.TLSConfig = v[0]
		case "tenant":
			cfg.Tenant = v[0]
		case "warehouse":
			cfg.Warehouse = v[0]
		case "access_token":
			cfg.AccessToken = v[0]
		case "sslmode":
			cfg.SSLMode = v[0]
		default:
			cfg.Params[k] = v[0]
		}
		if err != nil {
			return err
		}
	}

	return
}

// ParseDSN parses the DSN string to a Config
func ParseDSN(dsn string) (*Config, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	cfg := NewConfig()

	if strings.HasSuffix(u.Scheme, "http") {
		cfg.SSLMode = SSL_MODE_DISABLE
	}

	if len(u.Path) > 1 {
		// skip '/'
		cfg.Database = u.Path[1:]
	}
	if u.User != nil {
		// it is expected that empty password will be dropped out on Parse and Format
		cfg.User = u.User.Username()
		if passwd, ok := u.User.Password(); ok {
			cfg.Password = passwd
		}
	}
	if err = cfg.AddParams(map[string][]string(u.Query())); err != nil {
		return nil, err
	}

	if _, _, err := net.SplitHostPort(u.Host); err == nil {
		cfg.Host = u.Host
	} else {
		switch cfg.SSLMode {
		case SSL_MODE_DISABLE:
			cfg.Host = net.JoinHostPort(u.Host, "80")
		default:
			cfg.Host = net.JoinHostPort(u.Host, "443")
		}
	}

	return cfg, nil
}
