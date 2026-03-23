package db

// TrustTier defines a named VM-capacity tier and related capabilities.
type TrustTier struct {
	Key            string
	DisplayName    string
	Description    string
	VMLimit        int // max number of VMs (-1 = unlimited)
	CPULimit       int // max vCPUs per VM (-1 = unlimited)
	RAMLimitMB     int // max RAM in MB per VM (-1 = unlimited)
	DiskLimitMB    int // max total disk in MB (-1 = unlimited)
	CanAccessAdmin bool
	Requestable    bool
}

var trustTiers = []TrustTier{
	{
		Key:            "newbie",
		DisplayName:    "Newbie",
		Description:    "Starter tier for a single small VM.",
		VMLimit:        1,
		CPULimit:       1,
		RAMLimitMB:     2048,
		DiskLimitMB:    5120,
		CanAccessAdmin: false,
		Requestable:    false,
	},
	{
		Key:            "citizen",
		DisplayName:    "Citizen",
		Description:    "Standard tier for a couple of modest VMs.",
		VMLimit:        2,
		CPULimit:       2,
		RAMLimitMB:     4096,
		DiskLimitMB:    25600,
		CanAccessAdmin: false,
		Requestable:    true,
	},
	{
		Key:            "admin",
		DisplayName:    "Admin",
		Description:    "Unlimited capacity for trusted admins.",
		VMLimit:        -1,
		CPULimit:       -1,
		RAMLimitMB:     -1,
		DiskLimitMB:    -1,
		CanAccessAdmin: true,
		Requestable:    true,
	},
}

var trustTierByKey = func() map[string]TrustTier {
	m := make(map[string]TrustTier, len(trustTiers))
	for _, tier := range trustTiers {
		m[tier.Key] = tier
	}
	return m
}()

// ListTrustTiers returns all configured trust tiers in display order.
func ListTrustTiers() []TrustTier {
	out := make([]TrustTier, len(trustTiers))
	copy(out, trustTiers)
	return out
}

// ListRequestableTrustTiers returns trust tiers users may request via Routussy.
func ListRequestableTrustTiers() []TrustTier {
	var out []TrustTier
	for _, tier := range trustTiers {
		if tier.Requestable {
			out = append(out, tier)
		}
	}
	return out
}

// GetTrustTier returns the tier by key, or newbie if unknown.
func GetTrustTier(level string) TrustTier {
	if tier, ok := trustTierByKey[level]; ok {
		return tier
	}
	return trustTierByKey["newbie"]
}

// CanAccessAdmin reports whether the given trust level grants admin-panel access.
func CanAccessAdmin(level string) bool {
	return GetTrustTier(level).CanAccessAdmin
}
