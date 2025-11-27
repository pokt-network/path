#!/bin/bash

# Generate Go and TypeScript SDKs from OpenAPI specification
# This script generates both Go and TypeScript SDKs for the Portal DB API
# - Go SDK: Uses oapi-codegen for client and models generation  
# - TypeScript SDK: Uses openapi-typescript for type generation and openapi-fetch for runtime client

set -e

# Configuration
OPENAPI_DIR="../openapi"
OPENAPI_V3_FILE="$OPENAPI_DIR/openapi.json"
GO_OUTPUT_DIR="../../sdk/go"
TS_OUTPUT_DIR="../../sdk/typescript"
CONFIG_MODELS="./codegen-models.yaml"
CONFIG_CLIENT="./codegen-client.yaml"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "🔧 Generating Go and TypeScript SDKs from OpenAPI specification..."

# ============================================================================
# PHASE 1: ENVIRONMENT VALIDATION
# ============================================================================
# Validate that all required tools are installed before proceeding

echo ""
echo -e "${BLUE}📋 Phase 1: Environment Validation${NC}"

# Check if Go is installed
if ! command -v go >/dev/null 2>&1; then
    echo -e "${RED}❌ Go is not installed. Please install Go first.${NC}"
    echo "   - Mac: brew install go"
    echo "   - Or download from: https://golang.org/"
    exit 1
fi

echo -e "${GREEN}✅ Go is installed: $(go version)${NC}"

# Check if oapi-codegen is installed
if ! command -v oapi-codegen >/dev/null 2>&1; then
    echo "📦 Installing oapi-codegen..."
    go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
    
    # Verify installation
    if ! command -v oapi-codegen >/dev/null 2>&1; then
        echo -e "${RED}❌ Failed to install oapi-codegen. Please check your Go installation.${NC}"
        exit 1
    fi
fi

echo -e "${GREEN}✅ oapi-codegen is available: $(oapi-codegen -version 2>/dev/null || echo 'installed')${NC}"

# Check if Node.js and npm are installed for TypeScript SDK generation
if ! command -v node >/dev/null 2>&1; then
    echo -e "${RED}❌ Node.js is not installed. Please install Node.js first.${NC}"
    echo "   - Mac: brew install node"
    echo "   - Or download from: https://nodejs.org/"
    exit 1
fi

echo -e "${GREEN}✅ Node.js is installed: $(node --version)${NC}"

if ! command -v npm >/dev/null 2>&1; then
    echo -e "${RED}❌ npm is not installed. Please install npm first.${NC}"
    exit 1
fi

echo -e "${GREEN}✅ npm is installed: $(npm --version)${NC}"

# Check if openapi-typescript is available
# We use npx which handles installation automatically if needed
echo -e "${GREEN}✅ Using npx for openapi-typescript (will auto-install if needed)${NC}"

# Check if configuration files exist
for config_file in "$CONFIG_MODELS" "$CONFIG_CLIENT"; do
    if [ ! -f "$config_file" ]; then
        echo -e "${RED}❌ Configuration file not found: $config_file${NC}"
        echo "   This should have been created as a permanent file."
        exit 1
    fi
done

echo -e "${GREEN}✅ Configuration files found: models, client${NC}"

# ============================================================================
# PHASE 2: OPENAPI SPEC GENERATION
# ============================================================================

echo ""
echo -e "${BLUE}📋 Phase 2: OpenAPI Spec Generation${NC}"

# Generate OpenAPI specification using the dedicated script
echo "📝 Generating OpenAPI specification..."
if ! ./generate-openapi.sh; then
    echo -e "${RED}❌ Failed to generate OpenAPI specification${NC}"
    exit 1
fi

echo -e "${GREEN}✅ OpenAPI specification ready: $OPENAPI_V3_FILE${NC}"

# ============================================================================
# PHASE 3: SDK GENERATION
# ============================================================================
# Generate both Go and TypeScript SDKs from the OpenAPI specification

echo ""
echo -e "${BLUE}📋 Phase 3: SDK Generation${NC}"

echo "🐹 Generating Go SDK in separate files for better readability..."

# Clean previous generated files
echo "🧹 Cleaning previous generated files..."
rm -f "$GO_OUTPUT_DIR/models.go" "$GO_OUTPUT_DIR/client.go"

# Generate models file (data types and structures)
echo "   Generating models.go..."
if ! oapi-codegen -config "$CONFIG_MODELS" "$OPENAPI_V3_FILE"; then
    echo -e "${RED}❌ Failed to generate models${NC}"
    exit 1
fi

# Generate client file (API client and methods)
echo "   Generating client.go..."
if ! oapi-codegen -config "$CONFIG_CLIENT" "$OPENAPI_V3_FILE"; then
    echo -e "${RED}❌ Failed to generate client${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Go SDK generated successfully in separate files${NC}"

echo ""
echo "🔷 Generating TypeScript SDK with openapi-typescript..."

# Create TypeScript output directory if it doesn't exist
mkdir -p "$TS_OUTPUT_DIR"

# Clean previous generated TypeScript files
echo "🧹 Cleaning previous TypeScript generated files..."
rm -f "$TS_OUTPUT_DIR/types.ts"

# Generate TypeScript types using openapi-typescript
echo "   Generating TypeScript types from OpenAPI spec..."
if ! npx --yes openapi-typescript "$OPENAPI_V3_FILE" -o "$TS_OUTPUT_DIR/types.ts"; then
    echo -e "${RED}❌ Failed to generate TypeScript types${NC}"
    exit 1
fi

echo -e "${GREEN}✅ TypeScript types generated successfully${NC}"

# ============================================================================
# PHASE 4: MODULE SETUP
# ============================================================================
# Initialize modules, install dependencies, and validate compilation

echo ""
echo -e "${BLUE}📋 Phase 4: Module Setup${NC}"

# Navigate to SDK directory for module setup
cd "$GO_OUTPUT_DIR"

# Run go mod tidy to resolve dependencies
echo "🔧 Resolving dependencies..."
if ! go mod tidy; then
    echo -e "${RED}❌ Failed to resolve Go dependencies${NC}"
    cd - >/dev/null
    exit 1
fi

echo -e "${GREEN}✅ Go dependencies resolved${NC}"

# Test compilation
echo "🔍 Validating generated code compilation..."
if ! go build ./...; then
    echo -e "${RED}❌ Generated code does not compile${NC}"
    cd - >/dev/null
    exit 1
fi

echo -e "${GREEN}✅ Generated code compiles successfully${NC}"

# Return to scripts directory
cd - >/dev/null

# TypeScript module setup
echo ""
echo "🔷 Setting up TypeScript module..."

# Navigate to TypeScript SDK directory
cd "$TS_OUTPUT_DIR"

# Create package.json if it doesn't exist
if [ ! -f "package.json" ]; then
    echo "📦 Creating package.json..."
    cat > package.json << 'EOF'
{
  "name": "@grovepath/portal-db-ts-sdk",
  "version": "1.0.0",
  "description": "TypeScript SDK for Grove Portal DB API",
  "type": "module",
  "main": "client.ts",
  "types": "types.ts",
  "files": [
    "client.ts",
    "types.ts",
    "README.md"
  ],
  "repository": {
    "type": "git",
    "url": "https://github.com/buildwithgrove/path.git",
    "directory": "portal-db/sdk/typescript"
  },
  "homepage": "https://github.com/pokt-network/path/tree/main/portal-db/sdk/typescript",
  "bugs": {
    "url": "https://github.com/pokt-network/path/issues"
  },
  "scripts": {
    "type-check": "tsc --noEmit"
  },
  "keywords": [
    "grove",
    "portal",
    "db",
    "api",
    "sdk",
    "typescript",
    "postgrest",
    "openapi",
    "type-safe"
  ],
  "author": "Grove Team",
  "license": "MIT",
  "dependencies": {
    "openapi-fetch": "^0.12.2"
  },
  "devDependencies": {
    "typescript": "^5.0.0",
    "openapi-typescript": "^7.4.3"
  },
  "peerDependencies": {
    "typescript": ">=5.0.0"
  }
}
EOF
fi

# Create a client.ts file that uses openapi-fetch
if [ ! -f "client.ts" ]; then
    echo "📝 Creating client.ts..."
    cat > client.ts << 'EOF'
/**
 * Grove Portal DB API Client
 * 
 * This client uses openapi-fetch for type-safe API requests.
 * It's lightweight with zero dependencies beyond native fetch.
 * 
 * @example
 * ```typescript
 * import createClient from './client';
 * 
 * const client = createClient({ baseUrl: 'http://localhost:3000' });
 * 
 * // GET request with full type safety
 * const { data, error } = await client.GET('/portal_accounts');
 * 
 * // POST request with typed body
 * const { data, error } = await client.POST('/portal_accounts', {
 *   body: { 
 *     portal_plan_type: 'PLAN_FREE',
 *     // ... other fields
 *   }
 * });
 * ```
 */
import createClient from 'openapi-fetch';
import type { paths } from './types';

export type { paths } from './types';

/**
 * Create a new API client instance
 * 
 * @param options - Client configuration options
 * @param options.baseUrl - Base URL for the API (default: http://localhost:3000)
 * @param options.headers - Default headers to include with every request
 * @returns Type-safe API client
 */
export default function createPortalDBClient(options?: {
  baseUrl?: string;
  headers?: HeadersInit;
}) {
  return createClient<paths>({
    baseUrl: options?.baseUrl || 'http://localhost:3000',
    headers: options?.headers,
  });
}

// Re-export for convenience
export { createClient };
EOF
fi

# Create tsconfig.json if it doesn't exist
if [ ! -f "tsconfig.json" ]; then
    echo "🔧 Creating tsconfig.json..."
    cat > tsconfig.json << 'EOF'
{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "lib": ["ES2020", "DOM"],
    "moduleResolution": "Bundler",
    "noUncheckedIndexedAccess": true,
    "esModuleInterop": true,
    "allowSyntheticDefaultImports": true,
    "strict": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "declaration": true,
    "declarationMap": true
  },
  "include": ["*.ts"],
  "exclude": ["node_modules", "dist"]
}
EOF
fi

echo -e "${GREEN}✅ TypeScript module setup completed${NC}"

# Install dependencies if package-lock.json doesn't exist
if [ ! -f "package-lock.json" ]; then
    echo "📦 Installing dependencies..."
    if npm install; then
        echo -e "${GREEN}✅ Dependencies installed successfully${NC}"
    else
        echo -e "${YELLOW}⚠️  Failed to install dependencies, but SDK was generated${NC}"
        echo "   Run 'npm install' in $TS_OUTPUT_DIR to install dependencies"
    fi
else
    echo -e "${GREEN}✅ Dependencies already installed${NC}"
fi

# Return to scripts directory
cd - >/dev/null

# ============================================================================
# SUCCESS SUMMARY
# ============================================================================

echo ""
echo -e "${GREEN}🎉 SDK generation completed successfully!${NC}"
echo ""
echo -e "${BLUE}📁 Generated Files:${NC}"
echo "   API Docs:     $OPENAPI_V3_FILE"
echo "   Go SDK:       $GO_OUTPUT_DIR"
echo "   TypeScript:   $TS_OUTPUT_DIR"
echo ""
echo -e "${BLUE}🐹 Go SDK:${NC}"
echo "   Module:   github.com/pokt-network/path/portal-db/sdk/go"
echo "   Package:  portaldb"
echo "   Files:"
echo "   • models.go       - Generated data models and types (updated)"
echo "   • client.go       - Generated SDK client and methods (updated)"
echo "   • go.mod          - Go module definition (permanent)"
echo "   • README.md       - Documentation (permanent)"
echo ""
echo -e "${BLUE}🔷 TypeScript SDK:${NC}"
echo "   Package:  @grovepath/portal-db-ts-sdk"
echo "   Runtime:  openapi-fetch (minimal dependency, uses native fetch)"
echo "   Files:"
echo "   • types.ts        - Generated TypeScript types from OpenAPI spec (updated)"
echo "   • client.ts       - Typed fetch client wrapper (permanent)"
echo "   • package.json    - Node.js package definition (permanent)"
echo "   • tsconfig.json   - TypeScript configuration (permanent)"
echo ""
echo -e "${BLUE}📚 API Documentation:${NC}"
echo "   • openapi.json    - OpenAPI 3.x specification (updated)"
echo ""
echo -e "${BLUE}🚀 Next Steps:${NC}"
echo ""
echo -e "${BLUE}Go SDK:${NC}"
echo "   1. Review generated models: cat $GO_OUTPUT_DIR/models.go | head -50"
echo "   2. Review generated client: cat $GO_OUTPUT_DIR/client.go | head -50" 
echo "   3. Import in your project: go get github.com/pokt-network/path/portal-db/sdk/go"
echo "   4. Check documentation: cat $GO_OUTPUT_DIR/README.md"
echo ""
echo -e "${BLUE}TypeScript SDK:${NC}"
echo "   1. Review generated types: cat $TS_OUTPUT_DIR/types.ts | head -50"
echo "   2. Review client wrapper: cat $TS_OUTPUT_DIR/client.ts"
echo "   3. Copy to your project or publish as npm package"
echo "   4. Import client: import createClient from './client'"
echo "   5. Use with type safety:"
echo "      const client = createClient({ baseUrl: 'http://localhost:3000' });"
echo "      const { data, error } = await client.GET('/portal_accounts');"
echo ""
echo -e "${BLUE}💡 Tips:${NC}"
echo "   • Go: Full client with methods, types separated for readability"
echo "   • TypeScript: openapi-fetch provides full type safety with minimal overhead"
echo "   • Both SDKs update automatically when you run this script"
echo "   • Run after database schema changes to stay in sync"
echo "   • TypeScript SDK uses openapi-fetch (1 dependency, tree-shakeable)"
echo "   • All request/response types are inferred from the OpenAPI spec"
echo ""
echo -e "${GREEN}✨ Happy coding!${NC}"