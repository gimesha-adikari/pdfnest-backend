package contact

import (
	"fmt"
	"strings"
	"time"

	"pdfnest-backend/config"

	"github.com/google/uuid"
)

type AdminService struct{}

func NewAdminService() *AdminService {
	return &AdminService{}
}

type AdminTicketQuery struct {
	Status   string
	Category string
	Priority string
	Search   string
	Sort     string
	Page     int
	Limit    int
}

type DashboardStats struct {
	Total       int64            `json:"total"`
	Open        int64            `json:"open"`
	InProgress  int64            `json:"in_progress"`
	WaitingUser int64            `json:"waiting_user"`
	Resolved    int64            `json:"resolved"`
	Closed      int64            `json:"closed"`
	Today       int64            `json:"today"`
	ThisWeek    int64            `json:"this_week"`
	ThisMonth   int64            `json:"this_month"`
	ByStatus    map[string]int64 `json:"by_status"`
	ByPriority  map[string]int64 `json:"by_priority"`
}

type TicketListResult struct {
	Items []config.ContactTicket `json:"items"`
	Total int64                  `json:"total"`
	Page  int                    `json:"page"`
	Limit int                    `json:"limit"`
}

type ReportsResult struct {
	StatusCounts   map[string]int64       `json:"status_counts"`
	PriorityCounts map[string]int64       `json:"priority_counts"`
	CategoryCounts []NameCount            `json:"category_counts"`
	RecentTickets  []config.ContactTicket `json:"recent_tickets"`
}

type NameCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type CategoryRequest struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Color       string `json:"color"`
	SortOrder   int    `json:"sort_order"`
	IsActive    bool   `json:"is_active"`
}

type ReplyRequest struct {
	Message string `json:"message"`
}

func (s *AdminService) Dashboard() (*DashboardStats, error) {
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startOfWeek := startOfDay.AddDate(0, 0, -int(startOfDay.Weekday()))
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	statuses := []string{"open", "in_progress", "waiting_user", "resolved", "closed"}
	byStatus := make(map[string]int64)
	byPriority := make(map[string]int64)

	for _, st := range statuses {
		var count int64
		_ = config.DB.Model(&config.ContactTicket{}).Where("status = ?", st).Count(&count).Error
		byStatus[st] = count
	}

	for _, pr := range []string{"low", "normal", "high", "urgent"} {
		var count int64
		_ = config.DB.Model(&config.ContactTicket{}).Where("priority = ?", pr).Count(&count).Error
		byPriority[pr] = count
	}

	var total, today, thisWeek, thisMonth int64
	_ = config.DB.Model(&config.ContactTicket{}).Count(&total).Error
	_ = config.DB.Model(&config.ContactTicket{}).Where("created_at >= ?", startOfDay).Count(&today).Error
	_ = config.DB.Model(&config.ContactTicket{}).Where("created_at >= ?", startOfWeek).Count(&thisWeek).Error
	_ = config.DB.Model(&config.ContactTicket{}).Where("created_at >= ?", startOfMonth).Count(&thisMonth).Error

	return &DashboardStats{
		Total:       total,
		Open:        byStatus["open"],
		InProgress:  byStatus["in_progress"],
		WaitingUser: byStatus["waiting_user"],
		Resolved:    byStatus["resolved"],
		Closed:      byStatus["closed"],
		Today:       today,
		ThisWeek:    thisWeek,
		ThisMonth:   thisMonth,
		ByStatus:    byStatus,
		ByPriority:  byPriority,
	}, nil
}

func (s *AdminService) ListTickets(q AdminTicketQuery) (*TicketListResult, error) {
	page := q.Page
	limit := q.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	offset := (page - 1) * limit

	tx := config.DB.Model(&config.ContactTicket{}).
		Preload("User").
		Preload("AssignedTo")

	if q.Status != "" {
		tx = tx.Where("status = ?", q.Status)
	}
	if q.Category != "" {
		tx = tx.Where("category = ?", q.Category)
	}
	if q.Priority != "" {
		tx = tx.Where("priority = ?", q.Priority)
	}
	if q.Search != "" {
		like := "%" + strings.TrimSpace(q.Search) + "%"
		tx = tx.Where(
			"ticket_number ILIKE ? OR email ILIKE ? OR subject ILIKE ? OR message ILIKE ? OR category ILIKE ?",
			like, like, like, like, like,
		)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, err
	}

	orderBy := "created_at DESC"
	switch strings.ToLower(strings.TrimSpace(q.Sort)) {
	case "oldest":
		orderBy = "created_at ASC"
	case "priority":
		orderBy = `
			CASE priority
				WHEN 'urgent' THEN 1
				WHEN 'high' THEN 2
				WHEN 'normal' THEN 3
				WHEN 'low' THEN 4
				ELSE 5
			END ASC, created_at DESC`
	case "updated":
		orderBy = "last_activity_at DESC NULLS LAST, created_at DESC"
	}

	var items []config.ContactTicket
	if err := tx.Order(orderBy).Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return nil, err
	}

	return &TicketListResult{
		Items: items,
		Total: total,
		Page:  page,
		Limit: limit,
	}, nil
}

func (s *AdminService) GetTicket(id string) (*config.ContactTicket, error) {
	var ticket config.ContactTicket
	if err := config.DB.Preload("User").Preload("AssignedTo").First(&ticket, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &ticket, nil
}

func (s *AdminService) UpdateStatus(id, status string) error {
	status = strings.TrimSpace(strings.ToLower(status))
	if status == "" {
		return fmt.Errorf("status is required")
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":           status,
		"last_activity_at": now,
		"updated_at":       now,
	}

	switch status {
	case "resolved":
		updates["resolved_at"] = now
		updates["closed_at"] = nil
	case "closed":
		updates["closed_at"] = now
	case "open", "in_progress", "waiting_user":
		updates["resolved_at"] = nil
		updates["closed_at"] = nil
	default:
		return fmt.Errorf("invalid status")
	}

	return config.DB.Model(&config.ContactTicket{}).
		Where("id = ?", id).
		Updates(updates).Error
}

func (s *AdminService) UpdatePriority(id, priority string) error {
	priority = strings.TrimSpace(strings.ToLower(priority))
	if priority == "" {
		return fmt.Errorf("priority is required")
	}

	switch priority {
	case "low", "normal", "high", "urgent":
	default:
		return fmt.Errorf("invalid priority")
	}

	return config.DB.Model(&config.ContactTicket{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"priority":         priority,
			"last_activity_at": time.Now(),
			"updated_at":       time.Now(),
		}).Error
}

func (s *AdminService) UpdateNotes(id, notes string) error {
	return config.DB.Model(&config.ContactTicket{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"internal_notes":   notes,
			"last_activity_at": time.Now(),
			"updated_at":       time.Now(),
		}).Error
}

func (s *AdminService) Reply(ticketID, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("message is required")
	}

	ticket, err := s.GetTicket(ticketID)
	if err != nil {
		return err
	}

	return s.sendReplyEmail(ticket, message)
}

func (s *AdminService) ListCategories() ([]config.ContactCategory, error) {
	var categories []config.ContactCategory
	if err := config.DB.Order("sort_order ASC, created_at ASC").Find(&categories).Error; err != nil {
		return nil, err
	}
	return categories, nil
}

func (s *AdminService) CreateCategory(req CategoryRequest) (*config.ContactCategory, error) {
	now := time.Now()
	category := &config.ContactCategory{
		ID:          uuid.New().String(),
		Name:        strings.TrimSpace(req.Name),
		Slug:        strings.TrimSpace(req.Slug),
		Type:        strings.TrimSpace(req.Type),
		Description: strings.TrimSpace(req.Description),
		Color:       strings.TrimSpace(req.Color),
		SortOrder:   req.SortOrder,
		IsActive:    req.IsActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if category.Name == "" || category.Slug == "" || category.Type == "" {
		return nil, fmt.Errorf("name, slug and type are required")
	}

	if err := config.DB.Create(category).Error; err != nil {
		return nil, err
	}

	return category, nil
}

func (s *AdminService) UpdateCategory(id string, req CategoryRequest) error {
	updates := map[string]interface{}{
		"name":        strings.TrimSpace(req.Name),
		"slug":        strings.TrimSpace(req.Slug),
		"type":        strings.TrimSpace(req.Type),
		"description": strings.TrimSpace(req.Description),
		"color":       strings.TrimSpace(req.Color),
		"sort_order":  req.SortOrder,
		"is_active":   req.IsActive,
		"updated_at":  time.Now(),
	}
	return config.DB.Model(&config.ContactCategory{}).Where("id = ?", id).Updates(updates).Error
}

func (s *AdminService) DeleteCategory(id string) error {
	return config.DB.Delete(&config.ContactCategory{}, "id = ?", id).Error
}

func (s *AdminService) Reports() (map[string]interface{}, error) {
	statuses := []string{"open", "in_progress", "waiting_user", "resolved", "closed"}
	prioritys := []string{"low", "normal", "high", "urgent"}

	statusCounts := map[string]int64{}
	priorityCounts := map[string]int64{}

	for _, st := range statuses {
		var count int64
		_ = config.DB.Model(&config.ContactTicket{}).Where("status = ?", st).Count(&count).Error
		statusCounts[st] = count
	}

	for _, pr := range prioritys {
		var count int64
		_ = config.DB.Model(&config.ContactTicket{}).Where("priority = ?", pr).Count(&count).Error
		priorityCounts[pr] = count
	}

	var categoryCounts []NameCount
	if err := config.DB.Model(&config.ContactTicket{}).
		Select("category as name, COUNT(*) as count").
		Group("category").
		Order("count DESC").
		Scan(&categoryCounts).Error; err != nil {
		return nil, err
	}

	var recent []config.ContactTicket
	_ = config.DB.Order("last_activity_at DESC NULLS LAST, created_at DESC").Limit(10).Find(&recent).Error

	return map[string]interface{}{
		"status_counts":   statusCounts,
		"priority_counts": priorityCounts,
		"category_counts": categoryCounts,
		"recent_tickets":  recent,
	}, nil
}
