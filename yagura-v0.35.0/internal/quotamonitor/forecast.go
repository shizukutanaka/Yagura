// forecast.go: linear quota depletion forecasting (v0.15.0)
//
// 動機:
//
//	v0.14 までは "EXHAUSTED になってから切替提案" の reactive design。
//	実用上は "あと 10 分で枯渇" を事前に知れれば、自然な区切りで安全に
//	handoff できる(行コミット完了後、新規 task 開始前など)。
//
// 仕組み:
//
//	各 Report 呼出を時系列の (timestamp, remaining_percent) として保持し、
//	直近 N 件(window=10)で線形回帰して slope を算出。
//	slope が負(消費中)で十分な信頼度があれば、predicted_empty_at を返す。
//
// 設計判断:
//   - ゼロ依存(ADR-0001、math 関数も使わず純粋四則演算)
//   - 線形のみ(対数 / 指数モデルは過剰設計)
//   - history は ring buffer 形式(最古を捨てる)
//   - confidence は サンプル数 + slope の単調性で簡易判定
//   - "回復(remaining 増加)" は forecast 無効(reset 直前など)
package quotamonitor

import (
	"fmt"
	"math"
	"time"
)

// ForecastWindowSize は forecast に使う直近サンプル数。
const ForecastWindowSize = 10

// MinForecastSamples は forecast 出力に必要な最小サンプル数。
const MinForecastSamples = 3

// ReportEvent は 1 回の Report 呼出を記録する。
type ReportEvent struct {
	At               time.Time
	RemainingPercent int
	Source           string
}

// Forecast は agent の枯渇予測を返す。
//
// 戻り値:
//
//	predictedEmptyAt: 予測 0% 到達時刻(ゼロ値なら予測不能)
//	confidence: 0.0-1.0、サンプル数 + slope 一貫性ベース
//	reason: 人間向け説明
//
// 予測不能(zero time)になる条件:
//   - サンプル数 < MinForecastSamples
//   - slope が非負(消費していない or 回復中)
//   - 既に EXHAUSTED(remaining=0)
type ForecastResult struct {
	PredictedEmptyAt time.Time `json:"predicted_empty_at,omitempty"`
	Confidence       float64   `json:"confidence"`
	Reason           string    `json:"reason"`
	SamplesUsed      int       `json:"samples_used"`
	SlopePerSecond   float64   `json:"slope_per_second"` // %/sec, 負なら消費中
}

// Forecast は agent の枯渇予測を返す。
func (m *Monitor) Forecast(agent Agent) ForecastResult {
	if !validAgent(agent) {
		return ForecastResult{Reason: "unknown agent"}
	}
	m.mu.RLock()
	history := append([]ReportEvent(nil), m.histories[agent]...) // copy under lock
	currentPercent := m.statuses[agent].RemainingPercent
	m.mu.RUnlock()

	if len(history) < MinForecastSamples {
		return ForecastResult{
			Reason:      fmt.Sprintf("insufficient data (%d samples, need %d)", len(history), MinForecastSamples),
			SamplesUsed: len(history),
		}
	}
	if currentPercent == 0 {
		return ForecastResult{
			Reason:      "already exhausted",
			SamplesUsed: len(history),
		}
	}

	// 線形回帰: y = a*t + b ここで y=remaining%, t=秒
	// origin を最初のサンプルにすれば数値安定性が増す
	origin := history[0].At
	var sumT, sumY, sumTT, sumTY float64
	n := float64(len(history))
	for _, ev := range history {
		t := ev.At.Sub(origin).Seconds()
		y := float64(ev.RemainingPercent)
		sumT += t
		sumY += y
		sumTT += t * t
		sumTY += t * y
	}
	denom := n*sumTT - sumT*sumT
	if denom == 0 {
		return ForecastResult{
			Reason:      "samples too clustered in time",
			SamplesUsed: len(history),
		}
	}
	slope := (n*sumTY - sumT*sumY) / denom
	intercept := (sumY - slope*sumT) / n

	if slope >= 0 {
		return ForecastResult{
			Reason:         "not depleting (slope >= 0); cannot forecast",
			SlopePerSecond: slope,
			SamplesUsed:    len(history),
		}
	}

	// y = slope*t + intercept で y=0 となる t を計算 → 0 = slope*t + intercept → t = -intercept/slope
	tEmpty := -intercept / slope
	predictedEmptyAt := origin.Add(time.Duration(tEmpty * float64(time.Second)))

	// confidence: サンプル数 + slope 一貫性
	conf := computeConfidence(history, slope, intercept)

	return ForecastResult{
		PredictedEmptyAt: predictedEmptyAt,
		Confidence:       conf,
		Reason: fmt.Sprintf("linear projection: %.4f%%/sec depletion → empty at %s",
			slope, predictedEmptyAt.UTC().Format(time.RFC3339)),
		SamplesUsed:    len(history),
		SlopePerSecond: slope,
	}
}

// computeConfidence は forecast の信頼度を 0..1 で返す。
//
// 加味する要素:
//   - サンプル数 (window 満タンが理想)
//   - R^2 風指標(線形当てはまりの良さ)
func computeConfidence(history []ReportEvent, slope, intercept float64) float64 {
	if len(history) < MinForecastSamples {
		return 0
	}

	// (1) サンプル充足度
	sampleScore := float64(len(history)) / float64(ForecastWindowSize)
	if sampleScore > 1.0 {
		sampleScore = 1.0
	}

	// (2) R^2 風: residual / total variance
	origin := history[0].At
	var sumY, sumYY, sumResSq float64
	n := float64(len(history))
	for _, ev := range history {
		y := float64(ev.RemainingPercent)
		sumY += y
		sumYY += y * y
		t := ev.At.Sub(origin).Seconds()
		pred := slope*t + intercept
		res := y - pred
		sumResSq += res * res
	}
	meanY := sumY / n
	totalSS := sumYY - n*meanY*meanY
	if totalSS == 0 {
		return sampleScore * 0.5 // 変化なし → 限定的信頼度
	}
	rSquared := 1.0 - sumResSq/totalSS
	if rSquared < 0 || math.IsNaN(rSquared) {
		rSquared = 0
	}
	if rSquared > 1 {
		rSquared = 1
	}

	// 重み付け平均: サンプル数 40% + R^2 60%
	return sampleScore*0.4 + rSquared*0.6
}
