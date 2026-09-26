package server

import (
	"context"
	"encoding/xml"
	"errors"
	"net"
	"testing"
	"time"

	"airshift/openmos/internal/config"
	"airshift/openmos/internal/model"
	"airshift/openmos/internal/repository"
	"airshift/openmos/internal/service"
	mosxml "airshift/openmos/internal/xml"
)

type listRepository struct {
	repository.RunningOrderRepository
	orders []*model.RunningOrder
	err    error
}

func (r listRepository) List(context.Context) ([]*model.RunningOrder, error) {
	return r.orders, r.err
}

func TestROReqAllDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, root string
		orders     []*model.RunningOrder
		err        error
		wantCount  int
	}{
		{"one", "roListAll", []*model.RunningOrder{{ID: "ro-1", Slug: "Sample", Duration: 30}}, nil, 1},
		{"empty", "roListAll", nil, nil, 0},
		{"error", "", nil, errors.New("unavailable"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := mosxml.ParseMessage("<roReqAll/>")
			if err != nil {
				t.Fatal(err)
			}
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			peer.SetReadDeadline(time.Now().Add(time.Second))
			mosService := service.NewMOSService(listRepository{orders: tc.orders, err: tc.err}, nil, nil, nil, nil)
			client := &ClientConnection{
				conn: local, id: "test",
				server: &TCPServer{service: mosService},
				config: &config.Config{},
			}
			client.config.Server.WriteTimeout = time.Second
			if tc.err != nil {
				if err := client.handleMessage(context.Background(), request); err == nil {
					t.Fatal("expected repository error")
				}
				return
			}
			errCh := make(chan error, 1)
			go func() { errCh <- client.handleMessage(context.Background(), request) }()
			var response struct {
				XMLName xml.Name
				Orders  []struct {
					ID       string `xml:"roID"`
					Duration string `xml:"roEdDur"`
				} `xml:"ro"`
			}
			if err := xml.NewDecoder(peer).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if err := <-errCh; err != nil {
				t.Fatal(err)
			}
			if response.XMLName.Local != tc.root || len(response.Orders) != tc.wantCount {
				t.Fatalf("unexpected response: %+v", response)
			}
			if tc.name == "one" && (response.Orders[0].ID != "ro-1" || response.Orders[0].Duration != "00:00:30") {
				t.Fatalf("unexpected summary: %+v", response.Orders[0])
			}
		})
	}
}
