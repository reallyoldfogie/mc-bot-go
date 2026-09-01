package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"runtime/debug"

	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/reallyoldfogie/mc-protocol-go/models"
)

// HandleGame receive server packet and response them correctly.
// Note that HandleGame will block if you don't receive from Events.
func (c *client) HandleGame(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			log.Println("Signal received through context, stopping work")
			return fmt.Errorf("interrupt signal received")

		default:
			var p pk.Packet
			// Read packets
			if err := c.conn.ReadPacket(&p); err != nil {
				if err2 := new(net.Error); errors.As(err, err2) {
					return fmt.Errorf("socket closed")
				}
				return err
			}

			if p.ID == int32(c.packetMgr.GetClientboundPacketID("ClientboundBundleDelimiter")) {
				// Bundle delimiter should always have empty payload
				log.Printf("[BUNDLE] Opening delimiter - PacketID=%d, DataLen=%d bytes", p.ID, len(p.Data))
				if len(p.Data) > 0 {
					log.Printf("[BUNDLE] WARNING: Bundle delimiter has non-empty payload! Data=%x", p.Data)
				}

				// Handle bundle - record bundled packets, not delimiter
				if err := c.handlePacket(p); err != nil {
					return err
				}

				err := c.handleBundlePackets()
				if err != nil {
					return err
				}

				// Return the delimiter packet buffer
				c.conn.pool.Put(p.Data)
			} else {
				// Record non-bundled packets to replay
				recordForReplay(c.replayRecorder, p)

				// handle packets
				err := c.handlePacket(p)
				if err != nil {
					return err
				}

				// return the packet buffer
				c.conn.pool.Put(p.Data)
			}
		}
	}
}

type PacketHandlerError struct {
	ID  models.ClientboundPacketID
	Err error
}

func (d PacketHandlerError) Error() string {
	return fmt.Sprintf("handle packet %v error: %v", d.ID, d.Err)
}

func (d PacketHandlerError) Unwrap() error {
	return d.Err
}

func (c *client) handleBundlePackets() (err error) {
	var packets []pk.Packet
	for range 4096 {
		var p pk.Packet
		// Read packets
		if err := c.conn.ReadPacket(&p); err != nil {
			return err
		}

		if p.ID == int32(c.packetMgr.GetClientboundPacketID("ClientboundBundleDelimiter")) {
			log.Printf("Received finishing ClientbundleDelimiter")
			// Do NOT record closing delimiter - same as opening delimiter, it's protocol-level only
			if err := c.handlePacket(p); err != nil {
				return err
			}
			// Return the closing delimiter packet buffer
			c.conn.pool.Put(p.Data)
			// bundle finished
			goto handlePackets
		}

		// Record bundled packets to replay (but not bundle delimiters, which are protocol-level only)
		recordForReplay(c.replayRecorder, p)
		packets = append(packets, p)
	}
	return errors.New("packet number of a bundle out of limit")

handlePackets:
	for i := range packets {
		log.Printf("Handling bundle packet[%d].ID = %d\n", i, packets[i].ID)
		if err := c.handlePacket(packets[i]); err != nil {
			return err
		}
		// Return the packet buffer to pool
		c.conn.pool.Put(packets[i].Data)
	}
	return nil
}

func (c *client) handlePacket(p pk.Packet) (err error) {
	packetID := models.ClientboundPacketID(p.ID)
	for _, handler := range c.events.GetGenericListeners() {
		if err = handler.F(p); err != nil {
			return PacketHandlerError{ID: packetID, Err: fmt.Errorf("[generic event handlers] %w", err)}
		}
	}
	if int(packetID) < len(c.events.GetListeners()) {
		for _, handler := range c.events.GetListeners()[packetID] {
			err = handler.F(p)
			if err != nil {
				return PacketHandlerError{ID: packetID, Err: fmt.Errorf("[event handlers] %w", err)}
			}
		}
	} else {
		log.Printf("[ERROR] unknown packetID: %v\n", packetID)
		debug.Stack()
	}
	return
}
