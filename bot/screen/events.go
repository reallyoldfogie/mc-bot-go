package screen

import "github.com/Tnze/go-mc/chat"

type ContainerEventsListener interface {
	Open(id int, container_type int32, title chat.Message) error
	SetSlot(id int, index int16) error
	Close(id int) error
}
