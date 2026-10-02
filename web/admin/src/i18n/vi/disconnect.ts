/** Disconnecting a guest, in Vietnamese. A total translation of `../disconnect.ts`. */
import type { Translation } from "../catalogue.js";
import type { disconnect } from "../disconnect.js";

export const vi: Translation<typeof disconnect> = {
  "disconnect.button": "Ngắt kết nối khách",
  "disconnect.confirm.title": "Ngắt kết nối khách này?",
  "disconnect.confirm.verb": "Ngắt kết nối",
  "disconnect.confirm.consequence": (mac: string) =>
    `Foyer yêu cầu thiết bị kết thúc phiên của ${mac}. Khách sẽ rời khỏi mạng.`,
  "disconnect.confirm.recovery":
    "Riêng việc này có thể khắc phục: khách có thể đăng nhập lại bằng cùng phiếu hoặc tài khoản. Đánh dấu bên dưới để ngăn điều đó.",
  "disconnect.option.voucher": "Thu hồi luôn phiếu mà phiên này đã dùng",
  "disconnect.option.grant": "Thu hồi luôn quyền truy cập qua cổng đăng nhập của thiết bị này",
  "disconnect.cannot.title": "Foyer không ngắt kết nối được khách trên thiết bị này",
  "disconnect.cannot.ruckus":
    "Ruckus SmartZone kết thúc phiên cổng đăng nhập qua Northbound API, mà Foyer không điều khiển; lệnh ngắt RADIUS thất bại ở đó. Hãy ngắt kết nối khách trong SmartZone.",
  "disconnect.cannot.omada": "Foyer chưa có trình điều khiển Omada. Hãy ngắt kết nối khách trong bộ điều khiển Omada.",
  "disconnect.cannot.unprobed":
    "Thiết bị này chưa báo rằng nó kết thúc được phiên. Hãy kiểm tra nó từ màn hình Điểm truy cập; trong lúc chờ, ngắt kết nối khách trên chính thiết bị.",
  "disconnect.done.title": "Đã ngắt kết nối khách",
  "disconnect.done.body": "Thiết bị đã xác nhận. Dòng sẽ ghi là đã kết thúc khi bản ghi kết thúc kế toán tới.",
  "disconnect.failed.title": "Khách vẫn đang trực tuyến",
  "disconnect.cause.refused": (errorCause: number | null) =>
    errorCause === 503
      ? "Thiết bị từ chối: nó không có phiên này (Error-Cause 503)."
      : errorCause
        ? `Thiết bị từ chối (Error-Cause ${errorCause}).`
        : "Thiết bị từ chối và không đưa ra lý do.",
  "disconnect.cause.no-answer":
    "Thiết bị không trả lời. Có thể không kết nối được, tường lửa chặn yêu cầu, hoặc cổng CoA của nó bị sai.",
  "disconnect.cause.bad-secret": "Có thiết bị trả lời, nhưng khoá bí mật dùng chung không khớp với khoá Foyer đang giữ.",
  "disconnect.cause.error": "Foyer không hoàn tất được yêu cầu tới thiết bị.",
  "disconnect.withdrawn": (voucher: boolean, grants: number) =>
    [
      voucher ? "Phiếu đã bị thu hồi." : "",
      grants > 0 ? `Đã thu hồi ${grants} quyền truy cập qua cổng đăng nhập.` : "",
    ]
      .filter(Boolean)
      .join(" "),
};
