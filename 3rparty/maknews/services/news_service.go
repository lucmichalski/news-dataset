package services

import (
	m "github.com/rinosukmandityo/maknews/models"
)

type NewsService interface {
	GetById(id int) (*m.News, error)
	Store(data *m.News) error
	Update(data map[string]interface{}, id int) (*m.News, error)
	Delete(id int) error
}
