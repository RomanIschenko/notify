package events

type EventType string
type Event struct {
	Data interface{}
	Type EventType
}
