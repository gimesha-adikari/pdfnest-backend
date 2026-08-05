package billing

var GuestQuota *GuestQuotaStore

func Initialize(guestQuota *GuestQuotaStore) {
	GuestQuota = guestQuota
}
