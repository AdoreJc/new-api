package setting

// Waffo Pancake hosted checkout configuration. Gateway is configured once
// MerchantID + PrivateKey + ProductID are populated; WaffoPancakeEnabled
// then switches new orders on or off (webhooks are processed either way).
// StoreID + ProductID are operator-bound via SaveWaffoPancakeConfig.
var (
	WaffoPancakeMerchantID string
	WaffoPancakePrivateKey string
	WaffoPancakeReturnURL  string
	WaffoPancakeUnitPrice  float64 = 1.0
	WaffoPancakeMinTopUp   int     = 1
	WaffoPancakeStoreID    string
	WaffoPancakeProductID  string
	WaffoPancakeEnabled    = true
)
