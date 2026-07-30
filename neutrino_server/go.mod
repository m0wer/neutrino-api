module github.com/m0wer/neutrino-api/neutrino_server

go 1.25.11

require (
	github.com/btcsuite/btcd v0.25.0
	github.com/btcsuite/btcd/btcutil v1.2.0
	github.com/btcsuite/btcd/chaincfg/chainhash v1.2.0
	github.com/btcsuite/btclog v1.0.0
	github.com/btcsuite/btcwallet/walletdb v1.6.0
	github.com/gorilla/mux v1.8.1
	github.com/lightninglabs/neutrino v0.17.1
	go.etcd.io/bbolt v1.5.0
	golang.org/x/net v0.57.0
)

replace github.com/lightninglabs/neutrino => github.com/m0wer/neutrino v0.0.0-20260522161657-0d5f911e647c

require (
	github.com/aead/siphash v1.0.1 // indirect
	github.com/btcsuite/btcd/btcec/v2 v2.3.5 // indirect
	github.com/btcsuite/btcd/v2transport v1.0.1 // indirect
	github.com/btcsuite/btcwallet/wtxmgr v1.5.0 // indirect
	github.com/btcsuite/go-socks v0.0.0-20170105172521-4720035b7bfd // indirect
	github.com/btcsuite/websocket v0.0.0-20150119174127-31079b680792 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/decred/dcrd/crypto/blake256 v1.0.1 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.3.0 // indirect
	github.com/decred/dcrd/lru v1.1.2 // indirect
	github.com/kcalvinalvin/anet v0.0.0-20251112173137-d8ddc1f6dbee // indirect
	github.com/kkdai/bstream v1.0.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/lightninglabs/neutrino/cache v1.1.4 // indirect
	github.com/lightningnetwork/lnd/clock v1.1.1 // indirect
	github.com/lightningnetwork/lnd/fn/v2 v2.0.8 // indirect
	github.com/lightningnetwork/lnd/queue v1.2.0 // indirect
	github.com/lightningnetwork/lnd/ticker v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/exp v0.0.0-20260727155853-b88d891fe743 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
