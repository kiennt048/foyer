/** The dashboard's trends and the exports, in Vietnamese. A total translation of `../reports.ts`. */
import type { Translation } from "../catalogue.js";
import type { reports } from "../reports.js";

export const vi: Translation<typeof reports> = {
  "trends.title": "Xu hướng",
  "trends.metric": "Số liệu",
  "trends.metric.guestDevices": "Thiết bị khách",
  "trends.metric.vouchersRedeemed": "Phiếu đã dùng",
  "trends.metric.bytes": "Dữ liệu",
  "trends.range": "Khoảng thời gian",
  "trends.range.day": "14 ngày qua",
  "trends.range.week": "12 tuần qua",
  "trends.failed.title": "Không tải được xu hướng",
  "trends.chart": (figure: string, period: string) => `${figure}, ${period}, mỗi cột một kỳ`,
  "trends.col.period": "Kỳ",
  "trends.col.value": "Giá trị",
  "trends.max": (value: string) => `Cao nhất: ${value}`,
  "trends.note.guestDevices":
    "Số thiết bị khác nhau thấy qua cổng đăng nhập hoặc trong dữ liệu kế toán. Một điện thoại dùng địa chỉ riêng được đếm một lần cho mỗi mạng, không phải một lần cho mỗi người.",
  "trends.note.vouchersRedeemed": "Phiếu theo ngày được dùng lần đầu.",
  "trends.note.bytes":
    "Dữ liệu từ các thiết bị gửi kế toán RADIUS, tính vào ngày phiên bắt đầu. Đây là mức tối thiểu, không phải đồng hồ đo.",
  "trends.atLeast": (value: string) => `ít nhất ${value}`,
  "trends.lowerBound": "Một bộ đếm đã bị tràn trong ít nhất một kỳ, nên các tổng đó là mức tối thiểu.",
  "export.title": "Xuất dữ liệu",
  "export.body":
    "Tệp CSV các bản ghi của địa điểm này trong khoảng ngày đã chọn. Mọi lần xuất đều được ghi vào nhật ký thao tác, và mã phiếu không bao giờ được xuất đầy đủ.",
  "export.from": "Từ",
  "export.to": "Đến",
  "export.sessions": "Xuất phiên",
  "export.vouchers": "Xuất phiếu",
  "export.guests": "Xuất khách",
  "export.failed": "Không xuất được",
};
