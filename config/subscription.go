package config

func IsProUser(userID string) bool {
	var sub Subscription

	err := DB.
		Where("user_id = ? AND status = ?", userID, "active").
		First(&sub).Error

	if err != nil {
		return false
	}

	return sub.Tier == "pro"
}
