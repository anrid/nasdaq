package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var (
	printer = message.NewPrinter(language.English)
)

func main() {
	symbols := pflag.StringSliceP("symbols", "s", []string{
		"AAPL",
		"MSFT",
		"AMZN",
		"TSLA",
		"META",
		"AMD",
		"GOOG",
	}, "Symbols / Tickers to DCA into")
	fromDate := pflag.StringP("from", "f", "2008-01-01", "Start DCA:ing from this date")
	toDate := pflag.StringP("to", "t", time.Now().Format("2006-01-02"), "Stop DCA:ing at this date")
	monthlyAmount := pflag.Float64P("amount", "a", 500.00, "Amount to invest every month")

	pflag.Parse()

	NewDCAPortfolio(*symbols, *fromDate, *toDate, Monthly, *monthlyAmount)
}

type Frequency int

const (
	Daily Frequency = iota + 1
	Weekly
	Monthly
)

type DCA struct {
	Symbol            string
	Units             float64
	InitialInvestment float64
	PurchaseFrequency Frequency
	PurchaseAmount    float64
	TotalInvested     float64
	TotalReturn       float64
	PNL               float64
	From              time.Time
	To                time.Time
}

type DCAPortfolio struct {
	Positions     []*DCA
	TotalInvested float64
	TotalReturn   float64
	PNL           float64
}

func NewDCAPortfolio(symbols []string, fromDate, toDate string, f Frequency, spend float64) {
	dp := new(DCAPortfolio)

	for _, symbol := range symbols {
		s := spend / float64(len(symbols)) // Divide spend equally across all assets
		d := NewDCA(symbol, fromDate, toDate, f, s)
		dp.Positions = append(dp.Positions, d)
	}

	var allSymbols []string
	var from, to time.Time

	for _, d := range dp.Positions {
		dp.TotalInvested += d.TotalInvested
		dp.TotalReturn += d.TotalReturn

		if from.IsZero() || from.After(d.From) {
			from = d.From
		}
		if to.IsZero() || to.Before(d.To) {
			to = d.To
		}

		allSymbols = append(allSymbols, d.Symbol)

		d.Print()
	}

	dp.PNL = ((dp.TotalReturn / dp.TotalInvested) - 1) * 100

	printer.Printf("Portfolio      : %s\n", strings.Join(allSymbols, ","))
	printer.Printf("Period         : %s - %s\n", from.Format("2006-01-02"), to.Format("2006-01-02"))
	printer.Printf("Total Invested : $%.f\n", dp.TotalInvested)
	printer.Printf("Total Return   : $%.f\n", dp.TotalReturn)
	printer.Printf("PNL            : %.02f %%\n\n", dp.PNL)
}

func NewDCA(symbol, fromDate, toDate string, f Frequency, spend float64) *DCA {
	from := ISODateToTime(fromDate)
	to := ISODateToTime(toDate)
	if from.After(to) {
		log.Panicf("from date %s is after to date %s", from, to)
	}

	d := &DCA{
		Symbol:            symbol,
		PurchaseFrequency: f,
		PurchaseAmount:    spend,
	}

	nd := GetNASDAQHistoricialDataCached(symbol, fromDate, toDate)

	firstAvailableTradeDate := NASDAQDateToTime(nd.Data.TradesTable.Rows[len(nd.Data.TradesTable.Rows)-1].Date)
	if from.Before(firstAvailableTradeDate) {
		from = firstAvailableTradeDate
	}

	d.From = from
	d.To = to
	var lastPrice float64

	for at := from; at.Before(to); {

		price := nd.PriceCloseToDate(at)
		// fmt.Printf("%s - date %s - price %.02f\n", symbol, at.Format("2006-01-02"), price)

		d.Units += d.PurchaseAmount / price
		d.TotalInvested += d.PurchaseAmount

		var next time.Time
		if d.PurchaseFrequency == Monthly {
			y := at.Year()
			m := at.Month() + 1
			if m == 13 {
				m = 1
				y++
			}
			next = time.Date(y, m, at.Day(), 0, 0, 0, 0, time.UTC)
		} else if d.PurchaseFrequency == Weekly {
			next = at.Add(7 * 24 * time.Hour)
		} else {
			next = at.Add(24 * time.Hour)
		}

		at = next
		lastPrice = price
	}

	d.TotalReturn += d.Units * lastPrice
	d.PNL = ((d.TotalReturn / d.TotalInvested) - 1) * 100

	return d
}

func (d *DCA) Print() {
	printer.Printf("Symbol         : %s\n", d.Symbol)
	printer.Printf("Period         : %s - %s\n", d.From.Format("2006-01-02"), d.To.Format("2006-01-02"))
	printer.Printf("Total Invested : $%.f\n", d.TotalInvested)
	printer.Printf("Total Return   : $%.f\n", d.TotalReturn)
	printer.Printf("PNL            : %.02f %%\n\n", d.PNL)
}

type Account struct {
	Symbol string
	Units  float64
}

func Dump(o interface{}) {
	j, _ := json.MarshalIndent(o, "", "  ")
	fmt.Println(string(j))
}

type NASDAQHistoricalAPIResponse struct {
	Data struct {
		Symbol       string
		TotalRecords int64 `json:"totalRecords"`
		TradesTable  struct {
			Rows []*TradingData
		} `json:"tradesTable"`
	}
}

type TradingData struct {
	Date   string
	Close  string
	Volume string
	Open   string
	High   string
	Low    string
}

func ISODateToTime(date string) time.Time {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	return t
}

func NASDAQDateToTime(date string) time.Time {
	t, err := time.Parse("01/02/2006", date)
	if err != nil {
		panic(err)
	}
	return t
}

func (t *TradingData) AvgPrice() float64 {
	return (USDStringToFloat(t.Open) +
		USDStringToFloat(t.Close) +
		USDStringToFloat(t.High) +
		USDStringToFloat(t.Low)) / 4
}

func USDStringToFloat(usd string) float64 {
	usd = strings.Replace(usd, "$", "", -1)
	v, err := strconv.ParseFloat(usd, 64)
	if err != nil {
		log.Panicf("could not convert value '%s' to float", usd)
	}
	return v
}

func (ndr *NASDAQHistoricalAPIResponse) PriceCloseToDate(d time.Time) float64 {
	current := ndr.Data.TradesTable.Rows[0]

	for _, r := range ndr.Data.TradesTable.Rows {
		t := NASDAQDateToTime(r.Date)
		if d.After(t) {
			break
		}
		current = r
	}

	return current.AvgPrice()
}

func GetNASDAQHistoricialDataCached(ticker, fromDate, toDate string) *NASDAQHistoricalAPIResponse {
	file := fmt.Sprintf("./%s-%s-%s.json", ticker, fromDate, toDate)
	_, err := os.Stat(file)
	if err == nil {
		data, err := os.ReadFile(file)
		if err != nil {
			panic(err)
		}

		ndr := new(NASDAQHistoricalAPIResponse)
		err = json.Unmarshal(data, ndr)
		if err != nil {
			panic(err)
		}

		return ndr
	}

	ndr := CallNASDAQHistoricialAPI(ticker, fromDate, toDate)

	if len(ndr.Data.TradesTable.Rows) > 0 {
		j, err := json.MarshalIndent(ndr, "", "  ")
		if err != nil {
			panic(err)
		}

		err = os.WriteFile(file, j, 0777)
		if err != nil {
			panic(err)
		}
	}

	return ndr
}

func CallNASDAQHistoricialAPI(ticker, fromDate, toDate string) (ndr *NASDAQHistoricalAPIResponse) {
	url := "https://api.nasdaq.com/api/quote/{ticker}/historical?assetclass=stocks&fromdate={fromDate}&limit=9999&todate={toDate}&random=50"

	url = strings.Replace(url, "{ticker}", strings.ToUpper(ticker), 1)
	url = strings.Replace(url, "{fromDate}", fromDate, 1)
	url = strings.Replace(url, "{toDate}", toDate, 1)

	r, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		panic(err)
	}

	r.Header.Add("accept", "application/json")
	r.Header.Add("accept-encoding", "gzip")
	r.Header.Add("accept-language", "en-US,en")
	r.Header.Add("origin", "https://www.nasdaq.com")
	r.Header.Add("referer", "https://www.nasdaq.com/")
	r.Header.Add("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")

	c := http.Client{}
	res, err := c.Do(r)
	if err != nil {
		panic(err)
	}

	gr, err := gzip.NewReader(res.Body)
	if err != nil {
		panic(err)
	}

	data, err := io.ReadAll(gr)
	if err != nil {
		panic(err)
	}

	max := len(data)
	if max > 1_000 {
		max = 1_000
	}

	fmt.Printf("Fetching URL: %s\n\n", url)
	fmt.Println(string(data[0:max]))
	fmt.Printf("\n\nRead %d chars\n", len(data))

	ndr = new(NASDAQHistoricalAPIResponse)
	err = json.Unmarshal(data, ndr)
	if err != nil {
		panic(err)
	}

	return ndr
}
