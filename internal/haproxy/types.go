package haproxy

// Data Plane API response structures
type APIInfo struct {
	API    APIVersion `json:"api"`
	System struct{}   `json:"system"`
}

type APIVersion struct {
	BuildDate string `json:"build_date"`
	Version   string `json:"version"`
}

type Backend struct {
	Name    string  `json:"name"`
	Balance Balance `json:"balance"`
	From    string  `json:"from,omitempty"`
}

type Balance struct {
	Algorithm string `json:"algorithm"`
}

type Server struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	Check       string `json:"check,omitempty"`
	CheckType   string `json:"check_type,omitempty"`   // "tcp", "http", "disabled"
	CheckPath   string `json:"check_path,omitempty"`   // HTTP check path
	CheckMethod string `json:"check_method,omitempty"` // HTTP check method
	CheckHost   string `json:"check_host,omitempty"`   // HTTP check host header
}

type RuntimeServer struct {
	Address          string `json:"address,omitempty"`
	AdminState       string `json:"admin_state,omitempty"`       // "ready", "drain", "maint"
	OperationalState string `json:"operational_state,omitempty"` // "up", "down", etc.
	Port             int    `json:"port,omitempty"`
	ServerID         int    `json:"server_id,omitempty"`
	ServerName       string `json:"server_name,omitempty"`
}

type Frontend struct {
	Name           string `json:"name"`
	DefaultBackend string `json:"default_backend,omitempty"`
	From           string `json:"from,omitempty"`
}

// Service classification for our connector
type ServiceType string

const (
	ServiceTypeCustom  ServiceType = "custom"  // Use existing static backend
	ServiceTypeDynamic ServiceType = "dynamic" // Create dynamic backend
	ServiceTypeStatic  ServiceType = "static"  // Ignore - no connector involvement
)

// Connector service representation
type Service struct {
	Name        string
	Type        ServiceType
	BackendName string
	Servers     []Server
	HealthCheck *HealthCheck
}

type HealthCheck struct {
	Path   string
	Method string
	Host   string
}

type BackendStrategy string

const (
	BackendStrategyCreateNew      BackendStrategy = "create_new"
	BackendStrategyUseExisting    BackendStrategy = "use_existing"
	BackendStrategyFailOnConflict BackendStrategy = "fail_on_conflict"
)

// Domain mapping types
type DomainMapping struct {
	Domain      string     `json:"domain"`
	BackendName string     `json:"backend_name"`
	Type        DomainType `json:"type"`
}

type DomainType string

const (
	DomainTypeExact  DomainType = "exact"  // exact domain match
	DomainTypePrefix DomainType = "prefix" // domain prefix match
	DomainTypeRegex  DomainType = "regex"  // regex pattern match
)

// DomainMapConfig holds configuration for domain map file management
type DomainMapConfig struct {
	FilePath string `json:"file_path"`
	Enabled  bool   `json:"enabled"`
}

// APIError represents an API error response
type APIError struct {
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	return e.Message
}

// ClientInterface defines the interface for HAProxy client operations
type ClientInterface interface {
	GetConfigVersion() (int, error)
	GetBackend(name string) (*Backend, error)
	CreateBackend(backend Backend, version int) (*Backend, error)
	GetServers(backendName string) ([]Server, error)
	CreateServer(backendName string, server *Server, version int) (*Server, error)
	DeleteServer(backendName, serverName string, version int) error
	
	// Runtime server management
	GetRuntimeServer(backendName, serverName string) (*RuntimeServer, error)
	SetServerState(backendName, serverName, adminState string) error
	DrainServer(backendName, serverName string) error
	ReadyServer(backendName, serverName string) error
	MaintainServer(backendName, serverName string) error
}
