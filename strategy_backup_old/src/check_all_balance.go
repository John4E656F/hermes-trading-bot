package main

import (
	"encoding/json"
	"fmt"

	"github.com/joho/godotenv"
)

func init() {
	// This runs when imported - check balance on startup
}

func CheckAllBalances() {
	_ = godotenv.Overload("../.env")
	client := NewBybitClient()

	endpoints := []string{
		"/v5/account/wallet-balance?accountType=UNIFIED",
		"/v5/account/wallet-balance?accountType=SPOT",
	}

	for _, ep := range endpoints {
		fmt.Printf("=== %s ===\n", ep)
		resp, err := client.GetPrivateRequest(ep)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		var result map[string]interface{}
		json.Unmarshal(resp, &result)
		pretty, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(pretty))
	}
}
