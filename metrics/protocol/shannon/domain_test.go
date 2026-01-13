package shannon

import (
	"testing"
)

func TestExtractDomainOrHost(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		expected    string
		expectError bool
	}{
		// Standard domains with proper TLD extraction
		{
			name:     "Standard HTTPS URL",
			rawURL:   "https://example.com/path",
			expected: "example.com",
		},
		{
			name:     "Standard HTTPS URL with subdomain",
			rawURL:   "https://api.example.com/path",
			expected: "example.com",
		},
		{
			name:     "Standard HTTP URL",
			rawURL:   "http://test.example.org",
			expected: "example.org",
		},
		{
			name:     "URL with port",
			rawURL:   "https://api.example.com:8080/path",
			expected: "example.com",
		},
		{
			name:     "URL with deep subdomain",
			rawURL:   "https://deep.nested.api.example.com/path",
			expected: "example.com",
		},

		// Real-world examples from the codebase
		{
			name:     "NodeFleet domain",
			rawURL:   "https://relayminer.shannon-mainnet.eu.nodefleet.net",
			expected: "nodefleet.net",
		},
		{
			name:     "DoPokT domain with port",
			rawURL:   "https://rm02-eu.dopokt.com:443",
			expected: "dopokt.com",
		},
		{
			name:     "Complex subdomain structure",
			rawURL:   "https://relay1.mainnet.provider.com:8545/v1",
			expected: "provider.com",
		},

		// IP addresses (publicsuffix treats them as domains)
		{
			name:     "IPv4 address with HTTPS",
			rawURL:   "https://192.168.1.1:8080/path",
			expected: "192.168.1.1", // IP addresses returned as-is
		},
		{
			name:     "IPv4 address with HTTP",
			rawURL:   "http://10.0.0.1:3000",
			expected: "10.0.0.1", // IP addresses returned as-is
		},
		{
			name:     "IPv6 address",
			rawURL:   "https://[::1]:8080/path",
			expected: "::1",
		},
		{
			name:        "IPv6 address with zone (invalid URL escape)",
			rawURL:      "https://[fe80::1%lo0]:8080/path",
			expectError: true,
		},
		{
			name:     "Public IPv4 address",
			rawURL:   "https://203.0.113.42:8545",
			expected: "203.0.113.42", // IP addresses returned as-is
		},

		// Localhost variants
		{
			name:     "localhost with HTTPS",
			rawURL:   "https://localhost:3000",
			expected: "localhost",
		},
		{
			name:     "localhost with HTTP",
			rawURL:   "http://localhost:8080/api",
			expected: "localhost",
		},
		{
			name:     "localhost.localdomain",
			rawURL:   "http://localhost.localdomain",
			expected: "localhost.localdomain",
		},
		{
			name:     "localhost subdomain",
			rawURL:   "https://api.localhost.test",
			expected: "localhost.test",
		},
		{
			name:     "LOCALHOST uppercase",
			rawURL:   "https://LOCALHOST:3000",
			expected: "LOCALHOST",
		},

		// Private/internal domains
		{
			name:     "Local domain",
			rawURL:   "https://myservice.local",
			expected: "myservice.local",
		},
		{
			name:     "Internal domain",
			rawURL:   "https://api.internal",
			expected: "api.internal",
		},
		{
			name:     "Corp domain",
			rawURL:   "https://service.corp",
			expected: "service.corp",
		},
		{
			name:     "Home domain",
			rawURL:   "https://nas.home",
			expected: "nas.home",
		},
		{
			name:     "LAN domain",
			rawURL:   "https://router.lan",
			expected: "router.lan",
		},
		{
			name:     "Subdomain of internal",
			rawURL:   "https://api.service.local:8080",
			expected: "api.service.local", // Internal domains returned as-is
		},
		{
			name:     "Internal with uppercase",
			rawURL:   "https://SERVICE.INTERNAL",
			expected: "SERVICE.INTERNAL",
		},

		// Single label hostnames
		{
			name:     "Single hostname with HTTPS",
			rawURL:   "https://relayminer1",
			expected: "relayminer1",
		},
		{
			name:     "Single hostname with HTTP",
			rawURL:   "http://server1:8080",
			expected: "server1",
		},
		{
			name:     "Single hostname uppercase",
			rawURL:   "https://RELAYMINER1",
			expected: "RELAYMINER1",
		},

		// Edge cases for unknown TLDs (fallback behavior)
		{
			name:     "Unknown TLD with subdomain",
			rawURL:   "https://api.service.unknowntld",
			expected: "service.unknowntld",
		},
		{
			name:     "Deep subdomain with unknown TLD",
			rawURL:   "https://deep.api.service.unknowntld",
			expected: "service.unknowntld",
		},
		{
			name:     "Custom TLD",
			rawURL:   "https://relay.customtld",
			expected: "relay.customtld",
		},

		// URLs with paths and query parameters
		{
			name:     "URL with complex path",
			rawURL:   "https://api.example.com/v1/endpoint?param=value",
			expected: "example.com",
		},
		{
			name:     "URL with fragment",
			rawURL:   "https://service.com/path#fragment",
			expected: "service.com",
		},
		{
			name:     "URL with auth",
			rawURL:   "https://user:pass@example.com/path",
			expected: "example.com",
		},

		// URLs without scheme (should auto-prepend https://)
		{
			name:     "Hostname without scheme",
			rawURL:   "mainnet.eu.nodefleet.net",
			expected: "nodefleet.net",
		},
		{
			name:     "Hostname without scheme - simple domain",
			rawURL:   "example.com",
			expected: "example.com",
		},
		{
			name:     "Hostname without scheme - with subdomain",
			rawURL:   "api.example.com",
			expected: "example.com",
		},
		{
			name:     "Hostname without scheme - with port",
			rawURL:   "api.example.com:8080",
			expected: "example.com",
		},
		{
			name:     "Hostname without scheme - deep subdomain",
			rawURL:   "relayminer.shannon-mainnet.eu.nodefleet.net",
			expected: "nodefleet.net",
		},

		// Error cases
		{
			name:        "Malformed URL - invalid scheme",
			rawURL:      "://invalid-url",
			expectError: true,
		},
		{
			name:        "Empty URL",
			rawURL:      "",
			expectError: true,
		},
		{
			name:        "URL without host",
			rawURL:      "file:///path/to/file",
			expectError: true,
		},
		{
			name:        "URL with only scheme",
			rawURL:      "https://",
			expectError: true,
		},
		{
			name:        "Invalid characters in URL",
			rawURL:      "https://invalid url.com",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractDomainOrHost(tt.rawURL)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestFallbackDomainExtraction(t *testing.T) {
	tests := []struct {
		name        string
		host        string
		expected    string
		expectError bool
	}{
		// NOTE: IP addresses, localhost, and internal domains are now handled
		// BEFORE fallbackDomainExtraction is called. These tests verify the function's
		// behavior if called directly (edge cases that shouldn't happen in production).

		// IPv6 addresses (no dots, so returned as-is)
		{
			name:     "IPv6 address",
			host:     "::1",
			expected: "::1",
		},
		{
			name:     "IPv6 with zone",
			host:     "fe80::1%lo0",
			expected: "fe80::1%lo0",
		},

		// Single-part hostnames (returned as-is)
		{
			name:     "localhost",
			host:     "localhost",
			expected: "localhost",
		},
		{
			name:     "LOCALHOST uppercase",
			host:     "LOCALHOST",
			expected: "LOCALHOST",
		},

		// Multi-part hostnames (fallback takes last 2 parts)
		{
			name:     "localhost.localdomain",
			host:     "localhost.localdomain",
			expected: "localhost.localdomain",
		},
		{
			name:     "localhost subdomain",
			host:     "api.localhost.test",
			expected: "localhost.test", // Last 2 parts
		},
		{
			name:     "local TLD",
			host:     "myservice.local",
			expected: "myservice.local",
		},
		{
			name:     "internal TLD",
			host:     "api.internal",
			expected: "api.internal",
		},
		{
			name:     "corp TLD",
			host:     "service.corp",
			expected: "service.corp",
		},
		{
			name:     "home TLD",
			host:     "nas.home",
			expected: "nas.home",
		},
		{
			name:     "lan TLD",
			host:     "router.lan",
			expected: "router.lan",
		},

		// Single label hosts
		{
			name:     "single hostname",
			host:     "relayminer1",
			expected: "relayminer1",
		},
		{
			name:     "single hostname uppercase",
			host:     "RELAYMINER1",
			expected: "RELAYMINER1",
		},
		{
			name:     "single hostname with numbers",
			host:     "server123",
			expected: "server123",
		},

		// Multi-part domains that publicsuffix doesn't recognize
		{
			name:     "two parts unknown TLD",
			host:     "example.unknowntld",
			expected: "example.unknowntld",
		},
		{
			name:     "three parts unknown TLD",
			host:     "api.example.unknowntld",
			expected: "example.unknowntld",
		},
		{
			name:     "many parts unknown TLD",
			host:     "deep.nested.api.example.unknowntld",
			expected: "example.unknowntld",
		},
		{
			name:     "custom internal domain",
			host:     "service.customdomain",
			expected: "service.customdomain",
		},
		{
			name:     "deep custom domain",
			host:     "api.v1.service.customdomain",
			expected: "service.customdomain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := fallbackDomainExtraction(tt.host)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		// Positive cases
		{
			name:     "localhost",
			host:     "localhost",
			expected: true,
		},
		{
			name:     "LOCALHOST uppercase",
			host:     "LOCALHOST",
			expected: true,
		},
		{
			name:     "Localhost mixed case",
			host:     "Localhost",
			expected: true,
		},
		{
			name:     "localhost.localdomain",
			host:     "localhost.localdomain",
			expected: true,
		},
		{
			name:     "LOCALHOST.LOCALDOMAIN uppercase",
			host:     "LOCALHOST.LOCALDOMAIN",
			expected: true,
		},
		{
			name:     "localhost subdomain",
			host:     "localhost.test",
			expected: true,
		},
		{
			name:     "localhost deep subdomain",
			host:     "localhost.example.com",
			expected: true,
		},
		{
			name:     "api.localhost.test",
			host:     "api.localhost.test",
			expected: false,
		},
		{
			name:     "service.localhost.dev",
			host:     "service.localhost.dev",
			expected: false,
		},

		// Negative cases
		{
			name:     "not localhost - example.com",
			host:     "example.com",
			expected: false,
		},
		{
			name:     "not localhost - localhosted.com",
			host:     "localhosted.com",
			expected: false,
		},
		{
			name:     "not localhost - mylocalhost",
			host:     "mylocalhost",
			expected: false,
		},
		{
			name:     "not localhost - localhost-server.com",
			host:     "localhost-server.com",
			expected: false,
		},
		{
			name:     "not localhost - server.localhost.com",
			host:     "server.localhost.com",
			expected: false,
		},
		{
			name:     "not localhost - localhostname.com",
			host:     "localhostname.com",
			expected: false,
		},
		{
			name:     "empty string",
			host:     "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLocalhost(tt.host)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsPrivateOrInternalDomain(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		// Internal TLDs - positive cases
		{
			name:     "local TLD",
			host:     "service.local",
			expected: true,
		},
		{
			name:     "internal TLD",
			host:     "api.internal",
			expected: true,
		},
		{
			name:     "corp TLD",
			host:     "service.corp",
			expected: true,
		},
		{
			name:     "home TLD",
			host:     "nas.home",
			expected: true,
		},
		{
			name:     "lan TLD",
			host:     "router.lan",
			expected: true,
		},

		// Case insensitive
		{
			name:     "LOCAL uppercase",
			host:     "service.LOCAL",
			expected: true,
		},
		{
			name:     "INTERNAL uppercase",
			host:     "api.INTERNAL",
			expected: true,
		},
		{
			name:     "Mixed case local",
			host:     "service.Local",
			expected: true,
		},

		// Subdomains of internal TLDs
		{
			name:     "subdomain of local",
			host:     "api.service.local",
			expected: true,
		},
		{
			name:     "deep subdomain of internal",
			host:     "v1.api.service.internal",
			expected: true,
		},
		{
			name:     "subdomain of corp",
			host:     "mail.company.corp",
			expected: true,
		},

		// Single label domains (no dots) - positive cases
		{
			name:     "single hostname",
			host:     "relayminer1",
			expected: true,
		},
		{
			name:     "single hostname uppercase",
			host:     "RELAYMINER1",
			expected: true,
		},
		{
			name:     "single hostname with numbers",
			host:     "server123",
			expected: true,
		},
		{
			name:     "single hostname mixed case",
			host:     "ServerName",
			expected: true,
		},

		// Public domains - negative cases
		{
			name:     "example.com",
			host:     "example.com",
			expected: false,
		},
		{
			name:     "api.example.org",
			host:     "api.example.org",
			expected: false,
		},
		{
			name:     "nodefleet.net",
			host:     "relayminer.nodefleet.net",
			expected: false,
		},
		{
			name:     "google.com",
			host:     "mail.google.com",
			expected: false,
		},
		{
			name:     "github.com",
			host:     "api.github.com",
			expected: false,
		},

		// Edge cases - negative
		{
			name:     "not internal - localhost.com",
			host:     "localhost.com",
			expected: false,
		},
		{
			name:     "not internal - internal.com",
			host:     "internal.com",
			expected: false,
		},
		{
			name:     "not internal - localnet.com",
			host:     "localnet.com",
			expected: false,
		},
		{
			name:     "empty string",
			host:     "",
			expected: true,
		},

		// Special cases
		{
			name:     "single char hostname",
			host:     "a",
			expected: true,
		},
		{
			name:     "numeric hostname",
			host:     "123",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPrivateOrInternalDomain(tt.host)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestExtractDomainOrHost_RealWorldScenarios(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		expected    string
		description string
	}{
		{
			name:        "NordFleet",
			rawURL:      "https://skyrim.belongs-to-the.eu.nordfleet.net",
			expected:    "nordfleet.net",
			description: "The northern most province of Skyrim",
		},
		{
			name:        "DoIt with port",
			rawURL:      "https://rm02-eu.doit.com:443",
			expected:    "doit.com",
			description: "DoIt provider with very explicit HTTPS port",
		},
		{
			name:        "Self-hosted relay miner",
			rawURL:      "https://relay.mycompany.internal:8545",
			expected:    "relay.mycompany.internal", // Internal domains returned as-is
			description: "Self-hosted internal relay miner",
		},
		{
			name:        "Development localhost",
			rawURL:      "http://localhost:3000",
			expected:    "localhost",
			description: "Local development environment",
		},
		{
			name:        "IP-based endpoint",
			rawURL:      "https://203.0.113.42:8545",
			expected:    "203.0.113.42", // IP addresses returned as-is
			description: "Direct IP address endpoint",
		},
		{
			name:        "Single hostname relay",
			rawURL:      "https://relayminer1",
			expected:    "relayminer1",
			description: "Single hostname without domain (internal network)",
		},
		{
			name:        "Complex provider infrastructure",
			rawURL:      "https://relay-01.us-east.provider.net:8545",
			expected:    "provider.net",
			description: "Provider with geographic distribution",
		},
		{
			name:        "Development with port",
			rawURL:      "http://dev-server.local:8080",
			expected:    "dev-server.local",
			description: "Development server on local network",
		},
		{
			name:        "Corporate internal",
			rawURL:      "https://blockchain-node.corp:9545/rpc",
			expected:    "blockchain-node.corp",
			description: "Corporate internal blockchain node",
		},
		{
			name:        "Home lab setup",
			rawURL:      "https://pi-node.home:8545",
			expected:    "pi-node.home",
			description: "Home lab Raspberry Pi node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractDomainOrHost(tt.rawURL)

			if err != nil {
				t.Errorf("Unexpected error for %s: %v", tt.description, err)
				return
			}

			if result != tt.expected {
				t.Errorf("For %s: expected %q, got %q", tt.description, tt.expected, result)
			}
		})
	}
}
