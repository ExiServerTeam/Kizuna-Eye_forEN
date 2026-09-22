package transport

import (
	"context"

	"Kizuna-Eye/pkg/status"
)

// Sender はステータスを送信するインターフェース（エージェント側が実装）
type Sender interface {
	// Send はステータスを送信する
	Send(ctx context.Context, s *status.SystemStatus) error
}

// Receiver はステータスを受信するインターフェース（ダッシュボード側が実装）
type Receiver interface {
	// Receive はステータスを受信する（ブロッキング or 即時返却は実装依存）
	Receive(ctx context.Context) (*status.SystemStatus, error)
}

// Closer はリソースを解放するインターフェース
type Closer interface {
	Close() error
}

// Transport は Sender + Receiver を組み合わせた完全な通信インターフェース
// （双方向通信が必要な場合に使用。主に WebSocket 向け）
type Transport interface {
	Sender
	Receiver
	Closer
}
