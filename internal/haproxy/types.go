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
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Check   string `json:"check,omitempty"`
}

type Frontend struct {
	Name         string `json:"name"`
	DefaultBackend string `json:"default_backend,omitempty"`
	From         string `json:"from,omitempty"`
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