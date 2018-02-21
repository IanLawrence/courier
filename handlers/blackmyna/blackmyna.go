package blackmyna

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/buger/jsonparser"
	"github.com/nyaruka/courier"
	"github.com/nyaruka/courier/handlers"
	"github.com/nyaruka/courier/utils"
	"github.com/nyaruka/gocommon/urns"
	"github.com/pkg/errors"
)

var sendURL = "http://api.blackmyna.com/2/smsmessaging/outbound"

type handler struct {
	handlers.BaseHandler
}

func newHandler() courier.ChannelHandler {
	return &handler{handlers.NewBaseHandler(courier.ChannelType("BM"), "Blackmyna")}
}

func init() {
	courier.RegisterHandler(newHandler())
}

// Initialize is called by the engine once everything is loaded
func (h *handler) Initialize(s courier.Server) error {
	h.SetServer(s)
	err := s.AddHandlerRoute(h, http.methodGet, "receive", h.ReceiveMessage)
	if err != nil {
		return err
	}

	return s.AddHandlerRoute(h, http.methodGet, "status", h.StatusMessage)
}

// ReceiveMessage is our HTTP handler function for incoming messages
func (h *handler) ReceiveMessage(ctx context.Context, channel courier.Channel, w http.ResponseWriter, r *http.Request) ([]courier.Event, error) {
	// get our params
	bmMsg := &bmMessage{}
	err := handlers.DecodeAndValidateForm(bmMsg, r)
	if err != nil {
		return nil, err
	}

	// create our URN
	urn := urns.NewTelURNForCountry(bmMsg.From, channel.Country())

	// build our msg
	msg := h.Backend().NewIncomingMsg(channel, urn, bmMsg.Text)

	// and finally queue our message
	err = h.Backend().WriteMsg(ctx, msg)
	if err != nil {
		return nil, err
	}

	return []courier.Event{msg}, courier.WriteMsgSuccess(ctx, w, r, []courier.Msg{msg})
}

type bmMessage struct {
	To   string `validate:"required" name:"to"`
	Text string `validate:"required" name:"text"`
	From string `validate:"required" name:"from"`
}

var bmStatusMapping = map[int]courier.MsgStatusValue{
	1:  courier.MsgDelivered,
	2:  courier.MsgFailed,
	8:  courier.MsgSent,
	16: courier.MsgFailed,
}

// StatusMessage is our HTTP handler function for status updates
func (h *handler) StatusMessage(ctx context.Context, channel courier.Channel, w http.ResponseWriter, r *http.Request) ([]courier.Event, error) {
	// get our params
	bmStatus := &bmStatus{}
	err := handlers.DecodeAndValidateForm(bmStatus, r)
	if err != nil {
		return nil, err
	}

	msgStatus, found := bmStatusMapping[bmStatus.Status]
	if !found {
		return nil, fmt.Errorf("unknown status '%d', must be one of 1, 2, 8 or 16", bmStatus.Status)
	}

	// write our status
	status := h.Backend().NewMsgStatusForExternalID(channel, bmStatus.ID, msgStatus)
	err = h.Backend().WriteMsgStatus(ctx, status)
	if err != nil {
		return nil, err
	}

	return []courier.Event{status}, courier.WriteStatusSuccess(ctx, w, r, []courier.MsgStatus{status})
}

// SendMsg sends the passed in message, returning any error
func (h *handler) SendMsg(ctx context.Context, msg courier.Msg) (courier.MsgStatus, error) {
	username := msg.Channel().StringConfigForKey(courier.ConfigUsername, "")
	if username == "" {
		return nil, fmt.Errorf("no username set for BM channel")
	}

	password := msg.Channel().StringConfigForKey(courier.ConfigPassword, "")
	if password == "" {
		return nil, fmt.Errorf("no password set for BM channel")
	}

	apiKey := msg.Channel().StringConfigForKey(courier.ConfigAPIKey, "")
	if apiKey == "" {
		return nil, fmt.Errorf("no API key set for BM channel")
	}

	// build our request
	form := url.Values{
		"address":       []string{msg.URN().Path()},
		"senderaddress": []string{msg.Channel().Address()},
		"message":       []string{handlers.GetTextAndAttachments(msg)},
	}

	req, err := http.NewRequest(http.MethodPost, sendURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(username, password)
	rr, err := utils.MakeHTTPRequest(req)

	// record our status and log
	status := h.Backend().NewMsgStatusForID(msg.Channel(), msg.ID(), courier.MsgErrored)
	status.AddLog(courier.NewChannelLogFromRR("Message Sent", msg.Channel(), msg.ID(), rr).WithError("Message Send Error", err))
	if err != nil {
		return status, nil
	}

	// get our external id
	externalID, _ := jsonparser.GetString([]byte(rr.Body), "[0]", "id")
	if externalID == "" {
		return status, errors.Errorf("no external id returned in body")
	}

	status.SetStatus(courier.MsgWired)
	status.SetExternalID(externalID)

	return status, nil
}

type bmStatus struct {
	ID     string `validate:"required" name:"id"`
	Status int    `validate:"required" name:"status"`
}
