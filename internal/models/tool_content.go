package models

import (
	"time"
)

type DynamicToolItem struct {
	ID             uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Title          string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"title"`
	Description    string    `gorm:"type:text;not null" json:"description"`
	Href           string    `gorm:"type:varchar(255);not null;uniqueIndex" json:"href"`
	Category       string    `gorm:"type:varchar(50);not null" json:"category"`
	KeywordsJson   string    `gorm:"type:text;default:'[]'" json:"keywordsJson"`
	SeoTitle       string    `gorm:"type:varchar(255)" json:"seoTitle"`
	SeoDescription string    `gorm:"type:text" json:"seoDescription"`
	Intent         string    `gorm:"type:text" json:"intent"`
	RelatedJson    string    `gorm:"type:text;default:'[]'" json:"relatedJson"`
	FaqJson        string    `gorm:"type:text;default:'[]'" json:"faqJson"`
	FeaturesJson   string    `gorm:"type:text;default:'[]'" json:"featuresJson"`
	IsNew          bool      `gorm:"default:false" json:"isNew"`
	Accept         string    `gorm:"type:varchar(255)" json:"accept"`
	Multiple       bool      `gorm:"default:false" json:"multiple"`
	IconName       string    `gorm:"type:varchar(100);default:'FileText'" json:"iconName"`
	SortOrder      int       `gorm:"default:0" json:"sortOrder"`
	IsActive       bool      `gorm:"default:true" json:"isActive"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
