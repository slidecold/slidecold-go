package slidecold_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/slidecold/slidecold-go"
)

// fakeAPI stands in for https://slidecold.com/api/v1 so the examples run offline.
func fakeAPI() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /campaigns":
			_, _ = io.WriteString(w, `[{"id":"c1","name":"Spring outreach","status":"ACTIVE","totalSent":120,"totalReplied":9}]`)
		case "GET /campaigns/c1/stats":
			_, _ = io.WriteString(w, `{"totalSent":120,"totalPending":30,"totalReplied":9,"totalFailed":1,"totalSkipped":0,"replyRate":7,"templates":[]}`)
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"This API key is missing the campaigns:write scope"}`)
		}
	}))
}

func Example() {
	srv := fakeAPI()
	defer srv.Close()

	// In real code: slidecold.NewClient() reads SLIDECOLD_API_KEY or the config file.
	client := slidecold.NewClient(slidecold.WithAPIKey("cs_example"), slidecold.WithBaseURL(srv.URL))
	ctx := context.Background()

	campaigns, err := client.Campaigns.List(ctx, &slidecold.ListCampaignsParams{FolderID: "root"})
	if err != nil {
		log.Fatal(err)
	}
	for _, c := range campaigns {
		stats, err := client.Campaigns.Stats(ctx, c.ID)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s (%s): %d sent, %d%% reply rate\n", c.Name, c.Status, stats.TotalSent, stats.ReplyRate)
	}
	// Output: Spring outreach (ACTIVE): 120 sent, 7% reply rate
}

func ExampleIsPermissionError() {
	srv := fakeAPI()
	defer srv.Close()
	client := slidecold.NewClient(slidecold.WithAPIKey("cs_readonly"), slidecold.WithBaseURL(srv.URL))

	_, err := client.Campaigns.Create(context.Background(), &slidecold.CreateCampaignRequest{
		Name:        "Spring outreach",
		AudienceIDs: []string{"aud_1"},
		AccountIDs:  []string{"acc_1"},
		Templates:   []slidecold.CampaignTemplateInput{{Body: "Hey {{firstName}}!"}},
		DailyLimit:  slidecold.Ptr(25),
	})
	var apiErr *slidecold.Error
	if slidecold.IsPermissionError(err) && errors.As(err, &apiErr) {
		fmt.Println(apiErr.StatusCode, apiErr.Message)
	}
	// Output: 403 This API key is missing the campaigns:write scope
}

func ExampleVerifyWebhookSignature() {
	secret := "whsec_example"
	body := []byte(`{"event":"lead.imported"}`)

	// What SlideCold sends in the X-SlideCold-Signature header:
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	fmt.Println(slidecold.VerifyWebhookSignature(body, signature, secret))
	fmt.Println(slidecold.VerifyWebhookSignature([]byte(`{"event":"forged"}`), signature, secret))
	// Output:
	// true
	// false
}

func ExampleAccountsService_WaitForLoginJob() {
	client := slidecold.NewClient()
	ctx := context.Background()

	job, err := client.Accounts.Create(ctx, &slidecold.CreateAccountRequest{
		Username: "my_ig_account",
		Password: "hunter2",
		ProxyID:  slidecold.Ptr("proxy_1"),
	})
	if err != nil {
		log.Fatal(err)
	}
	status, err := client.Accounts.WaitForLoginJob(ctx, job.JobID, 2*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(status.State, string(status.Result))
}

func ExampleAudiencesService_Export() {
	client := slidecold.NewClient()
	csv, err := client.Audiences.Export(context.Background(), "aud_1")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(csv)
}
