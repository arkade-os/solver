package ports

// Balance is the wallet balance breakdown returned by the service layer.
type Balance struct {
	OnchainSpendable uint64
	OnchainLocked    uint64
	OffchainTotal    uint64
}

// Address holds the wallet addresses returned by the service layer.
type Address struct {
	OffchainAddress string
	BoardingAddress string
}
