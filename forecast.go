package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type DayDelta struct {
	Day   string
	Bytes uint64
}

func sortedDayWrites(m map[string]uint64) []DayDelta {
	out := make([]DayDelta, 0, len(m))
	for k, v := range m {
		out = append(out, DayDelta{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

func netGrowthSamples(samples []Sample) []DayDelta {
	if len(samples) < 3 {
		return nil
	}
	minFree := map[string]uint64{}
	for _, s := range samples {
		d := time.Unix(s.TS, 0).Format("2006-01-02")
		if cur, ok := minFree[d]; !ok || s.Free < cur {
			minFree[d] = s.Free
		}
	}
	days := make([]string, 0, len(minFree))
	for k := range minFree {
		days = append(days, k)
	}
	sort.Strings(days)
	var out []DayDelta
	for i := 1; i < len(days); i++ {
		if minFree[days[i-1]] > minFree[days[i]] {
			out = append(out, DayDelta{days[i], minFree[days[i-1]] - minFree[days[i]]})
		}
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func percentile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 1 {
		return s[0]
	}
	pos := float64(len(s)-1) * q
	lo := int(math.Floor(pos))
	hi := lo + 1
	if hi >= len(s) {
		hi = len(s) - 1
	}
	frac := pos - float64(lo)
	return s[lo] + (s[hi]-s[lo])*frac
}

type Forecast struct {
	OK        bool
	MedianMB  float64
	P10MB     float64
	P90MB     float64
	SlopeMB   float64
	DaysP50   float64
	DaysFast  float64
	DaysSlow  float64
	Source    string
	SampleDay int
}

func forecastFrom(free uint64, deltas []DayDelta, source string) Forecast {
	f := Forecast{Source: source}
	if len(deltas) == 0 {
		return f
	}
	vals := make([]float64, 0, len(deltas))
	for _, d := range deltas {
		vals = append(vals, float64(d.Bytes))
	}
	last := vals
	if len(last) > 14 {
		last = last[len(last)-14:]
	}
	med := median(last)
	q10 := percentile(last, 0.10)
	q90 := percentile(last, 0.90)
	f.OK = true
	f.MedianMB = med / 1e6
	f.P10MB = q10 / 1e6
	f.P90MB = q90 / 1e6
	f.SlopeMB = med / 1e6
	f.SampleDay = len(deltas)
	days := func(slope float64) float64 {
		if slope <= 0 {
			return math.Inf(1)
		}
		return float64(free) / slope
	}
	f.DaysP50 = days(med)
	f.DaysFast = days(q90)
	f.DaysSlow = days(q10)
	return f
}

type Anomaly struct {
	Kind  string
	Day   string
	Bytes uint64
	Text  string
}

func anomalies(deltas []DayDelta) []Anomaly {
	if len(deltas) < 5 {
		return nil
	}
	vals := make([]float64, len(deltas))
	for i, d := range deltas {
		vals[i] = float64(d.Bytes)
	}
	med := median(vals)
	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - med)
	}
	mad := median(dev)
	var out []Anomaly
	if mad > 0 {
		for _, d := range deltas {
			z := (float64(d.Bytes) - med) / (1.4826 * mad)
			if z > 3.5 && float64(d.Bytes) > 2*med && d.Bytes > 512<<20 {
				out = append(out, Anomaly{
					Kind: "spike", Day: d.Day, Bytes: d.Bytes,
					Text: fmt.Sprintf("%s 单日新增 %s，是常态的 %.1f 倍（多为安装/下载一次性行为）",
						d.Day[5:], human(d.Bytes), float64(d.Bytes)/math.Max(med, 1)),
				})
			}
		}
	}
	if len(vals) >= 10 {
		last7 := vals[len(vals)-7:]
		prev7 := vals[len(vals)-14 : len(vals)-7]
		hot := 0
		for _, v := range last7 {
			if v > 1.5*med {
				hot++
			}
		}
		if hot >= 4 && median(last7) > median(prev7)*1.25 {
			out = append(out, Anomaly{
				Kind: "leak", Day: deltas[len(deltas)-1].Day, Bytes: uint64(median(last7)),
				Text: fmt.Sprintf("最近 7 天平均每天新增 %s，比前一周快 %.0f%%（持续型增长，通常是缓存/日志在积累）",
					human(uint64(median(last7))), (median(last7)/math.Max(median(prev7), 1)-1)*100),
			})
		}
	}
	return out
}

// 剩余空间投影（未来 45 天）
func projection(free uint64, f Forecast, days int) (p50, fast, slow []float64) {
	freeGB := float64(free) / (1 << 30)
	for i := 0; i <= days; i++ {
		step50 := f.SlopeMB / 1024
		stepFast := f.P90MB / 1024
		stepSlow := f.P10MB / 1024
		p50 = append(p50, math.Max(0, freeGB-step50*float64(i)))
		fast = append(fast, math.Max(0, freeGB-stepFast*float64(i)))
		slow = append(slow, math.Max(0, freeGB-stepSlow*float64(i)))
	}
	return
}
