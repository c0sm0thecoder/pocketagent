package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Minimal Bot API calls for init and doctor, without starting the bot.

type tgUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

func tgCall(ctx context.Context, token, method string, params url.Values, out any) error {
	u := "https://api.telegram.org/bot" + token + "/" + method
	if params != nil {
		u += "?" + params.Encode()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("telegram: %s", r.Description)
	}
	return json.Unmarshal(r.Result, out)
}

func getMe(ctx context.Context, token string) (tgUser, error) {
	var u tgUser
	err := tgCall(ctx, token, "getMe", nil, &u)
	return u, err
}

// waitForFirstMessage long-polls until someone messages the bot and returns
// the sender. The update is consumed so the bot won't answer it later.
func waitForFirstMessage(ctx context.Context, token string) (tgUser, error) {
	offset := 0
	for {
		var updates []struct {
			UpdateID int `json:"update_id"`
			Message  *struct {
				From tgUser `json:"from"`
			} `json:"message"`
		}
		pctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		err := tgCall(pctx, token, "getUpdates", url.Values{"timeout": {"30"}, "offset": {fmt.Sprint(offset)}}, &updates)
		cancel()
		if ctx.Err() != nil {
			return tgUser{}, ctx.Err()
		}
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message != nil && u.Message.From.ID != 0 {
				tgCall(ctx, token, "getUpdates", url.Values{"offset": {fmt.Sprint(offset)}, "timeout": {"0"}}, &updates)
				return u.Message.From, nil
			}
		}
	}
}
