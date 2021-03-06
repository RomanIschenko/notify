package events

import (
	"github.com/RomanIschenko/notify"
	"github.com/RomanIschenko/notify/pubsub"
	"reflect"
)

type handler struct {
	rawHandler interface{}
	handlerVal reflect.Value
	appIndex   int
	clientIndex int
	dataIndex	int
	emitterIndex int
	data		interface{}
	codec		Codec
}

func (h *handler) call(app reflect.Value, emitter reflect.Value, client pubsub.Client, data []byte) {
	argsUsed := 0
	var args [4]reflect.Value
	if h.appIndex >= 0 {
		argsUsed++
		args[h.appIndex] = app
	}

	if h.clientIndex >= 0 {
		argsUsed++
		args[h.clientIndex] = reflect.ValueOf(client)
	}

	if h.dataIndex >= 0 {
		argsUsed++
		if err := h.codec.Unmarshal(data, h.data); err == nil {
			args[h.dataIndex] = reflect.ValueOf(h.data).Elem()
		} else {
			return
		}
	}

	if h.emitterIndex >= 0 {
		argsUsed++
		args[h.emitterIndex] = emitter
	}
	h.handlerVal.Call(args[:argsUsed])
}

func newHandler(hnd interface{}, codec Codec) *handler {
	t := reflect.TypeOf(hnd)
	if t.NumIn() > 4 {
		panic("handler can't have more than four arguments")
	}

	appType := reflect.TypeOf(&notify.App{})
	clientType := reflect.TypeOf((*pubsub.Client)(nil)).Elem()
	emitterType := reflect.TypeOf(&Emitter{})
	handlerVal := reflect.ValueOf(hnd)
	h := &handler{
		rawHandler:  hnd,
		handlerVal:  handlerVal,
		appIndex:    -1,
		clientIndex: -1,
		dataIndex:   -1,
		emitterIndex: -1,
		codec: codec,
	}

	for i := 0; i < t.NumIn(); i++ {
		paramType := t.In(i)
		if compareTypes(paramType, appType) {
			if h.appIndex >= 0 {
				panic("two apps in one handler")
			}
			h.appIndex = i
		} else if compareTypes(paramType, clientType) {
			if h.clientIndex >= 0 {
				panic("two clients in one handler")
			}
			h.clientIndex = i
		} else if compareTypes(paramType, emitterType) {
			if h.clientIndex >= 0 {
				panic("two emitters in one handler")
			}
			h.emitterIndex = i
		} else if h.dataIndex < 0 {
			h.data = reflect.New(paramType).Interface()
			h.dataIndex = i
		} else {
			panic("error in handler signature")
		}
	}
	return h
}
