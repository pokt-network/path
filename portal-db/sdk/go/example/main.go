package main

import (
	"context"
	"fmt"
	"log"

	portaldb "github.com/pokt-network/path/portal-db/sdk/go"
)

func main() {
	// Create client
	client, err := portaldb.NewClientWithResponses("http://localhost:3000")
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Get all networks
	networksResp, err := client.GetNetworksWithResponse(ctx, &portaldb.GetNetworksParams{})
	if err != nil {
		log.Fatal(err)
	}
	if networksResp.JSON200 != nil {
		networks := *networksResp.JSON200
		fmt.Printf("Networks: %d\n", len(networks))
	}

	// Filter active services
	servicesResp, err := client.GetServicesWithResponse(ctx, &portaldb.GetServicesParams{
		Active: func() *string { s := "eq.true"; return &s }(),
		Limit:  func() *string { s := "5"; return &s }(),
	})
	if err != nil {
		log.Fatal(err)
	}
	if servicesResp.JSON200 != nil {
		services := *servicesResp.JSON200
		fmt.Printf("Active services: %d\n", len(services))
		for _, service := range services {
			fmt.Printf("  %s\n", service.ServiceId)
		}
	}

	// Get specific service
	specificResp, err := client.GetServicesWithResponse(ctx, &portaldb.GetServicesParams{
		ServiceId: func() *string { s := "eq.ethereum-mainnet"; return &s }(),
	})
	if err != nil {
		log.Fatal(err)
	}
	if specificResp.JSON200 != nil && len(*specificResp.JSON200) > 0 {
		service := (*specificResp.JSON200)[0]
		fmt.Printf("Service: %s\n", service.ServiceName)
	}
}
