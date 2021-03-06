package pubsub

import (
	"context"
	"errors"
	"github.com/RomanIschenko/notify/internal/pubsub/internal/broadcaster"

	"log"
	"sync"
	"time"
)

type Config struct {
	InvalidationTime time.Duration
	CleanInterval    time.Duration
}

type PubSub struct {
	clients map[string]*client
	users map[string]*user
	topics map[string]*topic
	inactiveClients map[string]int64
	broadcaster	*broadcaster.Broadcaster
	config Config
	mu sync.RWMutex
}

func (p *PubSub) Clean() ([]Client, ChangeLog) {
	log.Println("cleaning")
	var clients []string
	now := time.Now().UnixNano()
	p.mu.Lock()
	for clientID, timestamp := range p.inactiveClients {
		if now - timestamp >= int64(p.config.InvalidationTime) {
			clients = append(clients, clientID)
		}
	}
	p.mu.Unlock()

	log.Println("clients to be deleted:", clients)

	return p.Disconnect(DisconnectOptions{
		Clients:   clients,
		TimeStamp: time.Now().UnixNano(),
	})
}

func (p *PubSub) Connect(opts ConnectOptions) (Client, ChangeLog, bool, error) {
	opts.validate()
	changelog := newChangeLog(opts.TimeStamp)

	mutator := p.broadcaster.Mutator(opts.TimeStamp)
	defer mutator.Close()

	isReconnected := false

	p.mu.Lock()
	c, clientExists := p.clients[opts.ClientID]
	if clientExists {
		isReconnected = true
		if c.user != opts.UserID {
			p.mu.Unlock()
			return nil, changelog, isReconnected, errors.New("failed to connect: user id mismatch")
		}
		delete(p.inactiveClients, c.id)
		log.Println("deleted from inactive clients ", c.id)
	} else {
		c = newClient(opts.ClientID, opts.UserID)
		p.clients[c.id] = c

		changelog.addCreatedClient(c.id)

		if opts.UserID != "" {
			u, userExists := p.users[opts.UserID]
			if !userExists {
				u = newUser()
				p.users[opts.UserID] = u
				changelog.addCreatedUser(opts.UserID)
			}

			u.Add(c)
			mutator.AttachUser(c.id, opts.UserID)
			for topicId, sub := range u.subscriptions.Map() {
				if !sub.Active {
					continue
				}

				// if sub is not active try to add for information about previous user subscriptions

				t := p.topics[topicId]
				if err := c.subscriptions.Add(topicId, opts.TimeStamp); err == nil {
					t.Add(c.id)
					mutator.Subscribe(c.id, topicId)
				}
			}
		}
	}
	mutator.UpdateClient(c.id, opts.Writer)
	p.mu.Unlock()

	return c, changelog, isReconnected, nil
}

func (p *PubSub) Inactivate(id string, ts int64) (Client, ChangeLog, error) {

	changelog := newChangeLog(ts)

	mutator := p.broadcaster.Mutator(ts)
	defer mutator.Close()

	p.mu.Lock()
	defer p.mu.Unlock()

	c, ok := p.clients[id]
	if !ok {
		return nil, changelog, errors.New("failed to inactivate: no client with such id")
	}
	p.inactiveClients[id] = ts

	changelog.addInactivatedClient(id)

	mutator.UpdateClient(id, nil)

	return c, changelog, nil
}

func (p *PubSub) Disconnect(opts DisconnectOptions) ([]Client, ChangeLog) {
	opts.validate()
	changelog := newChangeLog(opts.TimeStamp)

	var clients []Client

	mutator := p.broadcaster.Mutator(opts.TimeStamp)
	defer mutator.Close()

	p.mu.Lock()
	defer p.mu.Unlock()
	if opts.All {
		for clientID, c := range p.clients {
			mutator.DeleteClient(clientID)
			changelog.addDeletedClient(clientID)
			clients = append(clients, c)
			delete(p.clients, clientID)
			delete(p.inactiveClients, clientID)
			c.meta = nil
			if c.user != "" {
				if u, userExists := p.users[c.user]; userExists {
					u.Del(c.id)
					mutator.DetachUser(c.id, c.user, true)
					if len(u.clients) == 0 {
						changelog.addDeletedUser(c.user)
						delete(p.users, c.user)
					}
				}
			}
			for topicID := range c.subscriptions.Map() {
				mutator.Unsubscribe(c.id, topicID, true)
				if t, topicExists := p.topics[topicID]; topicExists {
					t.Del(topicID)
					if t.Len() == 0 {
						changelog.addDeletedTopic(topicID)
						delete(p.topics, topicID)
					}
				}
			}
		}
	} else {
		for _, clientID := range opts.Clients {
			mutator.DeleteClient(clientID)
			c, clientExists := p.clients[clientID]
			delete(p.inactiveClients, clientID)
			if !clientExists {
				continue
			}

			changelog.addDeletedClient(clientID)
			clients = append(clients, c)
			delete(p.clients, clientID)
			c.meta = nil
			if c.user != "" {
				if u, userExists := p.users[c.user]; userExists {
					u.Del(c.id)
					mutator.DetachUser(c.id, c.user, true)
					if len(u.clients) == 0 {
						changelog.addDeletedUser(c.user)
						delete(p.users, c.user)
					}
				}
			}
			for topicID := range c.subscriptions.Map() {
				mutator.Unsubscribe(c.id, topicID, true)
				if t, topicExists := p.topics[topicID]; topicExists {
					t.Del(c.id)
					if t.Len() == 0 {
						changelog.addDeletedTopic(topicID)
						delete(p.topics, topicID)
					}
				}
			}
		}
		for _, userID := range opts.Users {
			u, userExists := p.users[userID]

			if !userExists {
				continue
			}

			delete(p.users, userID)
			changelog.addDeletedUser(userID)

			for clientID, c := range u.clients {
				u.Del(clientID)
				mutator.DetachUser(c.id, userID, true)
				delete(p.clients, clientID)
				changelog.addDeletedClient(c.id)
				clients = append(clients, c)
				mutator.DeleteClient(c.id)
				c.meta = nil
				for topicID := range c.subscriptions.Map() {
					mutator.Unsubscribe(c.id, topicID, true)
					if t, topicExists := p.topics[topicID]; topicExists {
						t.Del(topicID)
						if t.Len() == 0 {
							changelog.addDeletedTopic(topicID)
							delete(p.topics, topicID)
						}
					}
				}
			}
		}
	}

	return clients, changelog
}

func (p *PubSub) Subscribe(opts SubscribeOptions) ChangeLog {
	opts.validate()
	changelog := newChangeLog(opts.TimeStamp)

	mutator := p.broadcaster.Mutator(opts.TimeStamp)
	defer mutator.Close()

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, topicID := range opts.Topics {
		t, topicExists := p.topics[topicID]

		if !topicExists {
			t = newTopic()
		}
		for _, clientID := range opts.Clients {
			c, clientExists := p.clients[clientID]
			if !clientExists {
				continue
			}
			if err := c.subscriptions.Add(topicID, opts.TimeStamp); err == nil {
				t.Add(c.id)
				mutator.Subscribe(c.id, topicID)
			} else {
				log.Println(err)
			}
		}
		for _, userID := range opts.Users {
			u, userExists := p.users[userID]
			if !userExists {
				continue
			}
			u.Subscribe(topicID, opts.TimeStamp, func(c *client) {
				t.Add(c.id)
				mutator.Subscribe(c.id, topicID)
			})
		}
		if !topicExists && t.Len() > 0 {
			changelog.addCreatedTopic(topicID)
			p.topics[topicID] = t
		}
	}

	return changelog
}

func (p *PubSub) Unsubscribe(opts UnsubscribeOptions) ChangeLog {
	opts.validate()
	changelog := newChangeLog(opts.TimeStamp)

	mutator := p.broadcaster.Mutator(opts.TimeStamp)
	defer mutator.Close()

	p.mu.Lock()
	defer p.mu.Unlock()

	if opts.All {
		for _, clientID := range opts.Clients {
			c, ok := p.clients[clientID]
			if !ok {
				continue
			}
			for topicId, _ := range c.subscriptions.Map() {
				if err := c.subscriptions.Delete(topicId, opts.TimeStamp); err == nil {
					mutator.Unsubscribe(c.id, topicId, false)
				}

				if t, topicExists := p.topics[topicId]; topicExists {
					t.Del(clientID)
					if t.Len() == 0 {
						delete(p.topics, topicId)
						changelog.addDeletedTopic(topicId)
					}
				}

			}
		}
		for _, userID := range opts.Users {
			u, ok := p.users[userID]
			if !ok {
				continue
			}

			for topicId := range u.subscriptions.Map() {
				u.Unsubscribe(topicId, opts.TimeStamp, func(c *client) {
					if t, ok := p.topics[topicId]; ok {
						t.Del(c.id)
						if t.Len() == 0 {
							delete(p.topics, topicId)
							changelog.addDeletedTopic(topicId)
						}
					}
					mutator.Unsubscribe(c.id, topicId, false)
				})
			}
		}
	} else if opts.AllFromTopic {
		for _, topicId := range opts.Topics {
			t, topicExists := p.topics[topicId]
			if !topicExists {
				continue
			}

			for id := range t.clients {
				c, clientExists := p.clients[id]
				if !clientExists {
					continue
				}
				// delete from users
				if c.user != "" {
					if u, userExists := p.users[c.user]; userExists {
						u.subscriptions.Delete(topicId, opts.TimeStamp)
					}
				}
				if err := c.subscriptions.Delete(topicId, opts.TimeStamp); err == nil {
					t.Del(c.id)
					mutator.Unsubscribe(c.id, topicId, false)
				}
			}

			if t.Len() == 0 {
				delete(p.topics, topicId)
				changelog.addDeletedTopic(topicId)
			}

		}
	} else {
		for _, topicId := range opts.Topics {
			t, topicExists := p.topics[topicId]
			if !topicExists {
				continue
			}
			for _, clientId := range opts.Clients {
				c, clientExists := p.clients[clientId]
				if !clientExists {
					continue
				}
				if err := c.subscriptions.Delete(topicId, opts.TimeStamp); err == nil {
					t.Del(c.id)
					mutator.Unsubscribe(c.id, topicId, false)
				}
			}
			for _, userId := range opts.Users {
				u, userExists := p.users[userId]
				if !userExists {
					continue
				}
				u.Unsubscribe(topicId, opts.TimeStamp, func(c *client) {
					t.Del(c.id)
					mutator.Unsubscribe(c.id, topicId, false)
				})
			}

			if t.Len() == 0 {
				delete(p.topics, topicId)
				changelog.addDeletedTopic(topicId)
			}
		}
	}

	return changelog
}

func (p *PubSub) Publish(opts PublishOptions) {
	opts.validate()
	p.broadcaster.Broadcast(opts.Clients, opts.Users, opts.Topics, opts.Data)
}

func (p *PubSub) Start(ctx context.Context) {
	p.broadcaster.Start(ctx)
}

func (p *PubSub) Metrics() Metrics {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return Metrics{
		Clients:         len(p.clients),
		Users:           len(p.users),
		Topics:          len(p.topics),
		InactiveClients: len(p.inactiveClients),
	}
}

func(p *PubSub) IsSubscribed(client string, topic string) (bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if cl, ok := p.clients[client]; ok {
		sub, ok := cl.subscriptions.Map()[topic]
		return sub.Active && ok, nil
	}
	return false, errors.New("no client found")
}

func(p *PubSub) IsUserSubscribed(user string, topic string) (bool, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if u, ok := p.users[user]; ok {
		sub, ok := u.subscriptions.Map()[topic]
		return sub.Active && ok, nil
	}
	return false, errors.New("no client found")
}

func(p *PubSub) TopicSubscribers(topic string) ([]string, error) {
	var subs []string
	p.mu.RLock()
	defer p.mu.RUnlock()
	if t, ok := p.topics[topic]; ok {
		subs = make([]string, 0, len(t.clients))
		for id, _ := range t.clients {
			subs = append(subs, id)
		}
	}

	return subs, errors.New("no topic found")

}

func(p *PubSub) ClientSubscriptions(id string) ([]string, error) {
	var subs []string
	p.mu.RLock()
	defer p.mu.RUnlock()
	if t, ok := p.clients[id]; ok {
		subs = make([]string, 0, len(t.subscriptions.Map()))
		for topicID, sub := range t.subscriptions.Map() {
			if sub.Active {
				subs = append(subs, topicID)
			}
		}
	}
	return subs, errors.New("no topic found")
}

func(p *PubSub) UserSubscriptions(id string) ([]string, error) {
	var subs []string
	p.mu.RLock()
	defer p.mu.RUnlock()
	if t, ok := p.users[id]; ok {
		subs = make([]string, 0, len(t.subscriptions.Map()))
		for topicID, sub := range t.subscriptions.Map() {
			if sub.Active {
				subs = append(subs, topicID)
			}
		}
	}
	return subs, errors.New("no topic found")
}


func New(cfg Config) *PubSub {
	return &PubSub{
		clients: map[string]*client{},
		users: map[string]*user{},
		topics: map[string]*topic{},
		inactiveClients: map[string]int64{},
		config: cfg,
		broadcaster:     broadcaster.New(),
	}
}