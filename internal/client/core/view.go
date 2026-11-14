package core

import (
	"cmp"
	"slices"

	"github.com/tolaniverse/lanternfolk/protocol"
)

// Selectors: read-only views over State that both UIs render (LLD §4.7).

// EntryStatus is how a timeline entry should look.
type EntryStatus uint8

const (
	Confirmed EntryStatus = iota // in the server's history
	InFlight                     // optimistic, not yet confirmed
	Rejected                     // optimistic, and the server (or validation) said no
)

// Entry is one line of a channel's timeline.
type Entry struct {
	Message protocol.Message
	Status  EntryStatus
	Err     string
}

// Timeline is the channel's confirmed messages, oldest first, followed by
// this user's pending ones in the order they were sent.
func (s State) Timeline(channelID string) []Entry {
	msgs := s.Messages[channelID]
	out := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Entry{Message: m})
	}
	for _, p := range s.PendingInOrder() {
		if p.ChannelID != channelID {
			continue
		}
		e := Entry{
			Message: protocol.Message{ChannelID: p.ChannelID, AuthorID: s.Auth.UserID,
				Body: p.Body, CreatedAt: p.At, ClientCmdID: p.ClientCmdID},
			Status: InFlight,
		}
		if p.Status == Failed {
			e.Status = Rejected
			if p.Err != nil {
				e.Err = cmp.Or(p.Err.Message, p.Err.Code)
			}
		}
		out = append(out, e)
	}
	return out
}

// PendingInOrder returns the pending commands in the order they were made.
func (s State) PendingInOrder() []Pending {
	out := make([]Pending, 0, len(s.Pending))
	for _, p := range s.Pending {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Pending) int { return cmp.Compare(a.Order, b.Order) })
	return out
}

// Handle returns the member's handle, or a short form of the ID if unknown.
func (s State) Handle(userID string) string {
	if userID == s.Auth.UserID && s.Auth.Handle != "" {
		return s.Auth.Handle
	}
	for _, m := range s.Members {
		if m.UserID == userID {
			return m.Handle
		}
	}
	if len(userID) > 8 {
		return userID[:8]
	}
	return userID
}

// RosterEntry is a member with their presence.
type RosterEntry struct {
	protocol.Member
	Online bool
}

// Roster lists members, online ones first, then by handle.
func (s State) Roster() []RosterEntry {
	out := make([]RosterEntry, 0, len(s.Members))
	for _, m := range s.Members {
		out = append(out, RosterEntry{Member: m, Online: s.Online[m.UserID]})
	}
	slices.SortFunc(out, func(a, b RosterEntry) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Handle, b.Handle)
	})
	return out
}

// DefaultChannel is #general if it exists, else the first channel, else "".
func (s State) DefaultChannel() string {
	for _, c := range s.Channels {
		if c.Name == "general" {
			return c.ID
		}
	}
	if len(s.Channels) > 0 {
		return s.Channels[0].ID
	}
	return ""
}
