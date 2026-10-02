/** `/guest-data` in Vietnamese. A total translation of `../guestdata.ts`. */
import type { Translation } from "../catalogue.js";
import type { guestdata } from "../guestdata.js";

export const vi: Translation<typeof guestdata> = {
  "guestData.fields.title": "Hỏi khách những gì",
  "guestData.fields.body":
    "Mọi biểu mẫu đăng nhập trên cổng đăng nhập đều có các trường bạn chọn ở đây — phiếu, tài khoản, đồng ý điều khoản và điện thoại. Một trường là không bắt buộc trừ khi bạn yêu cầu — và khách để trống trường bắt buộc sẽ không được vào mạng. Chỉ hỏi những gì bạn cần: mỗi trường là dữ liệu cá nhân bạn phải bảo vệ. Khách đăng nhập bằng điện thoại không bao giờ bị hỏi lại số điện thoại: số vừa được xác minh chính là số được lưu.",
  "guestData.field.name": "Họ tên",
  "guestData.field.phone": "Số điện thoại",
  "guestData.field.email": "Địa chỉ e-mail",
  "guestData.field.ask": "Hỏi",
  "guestData.field.require": "Bắt buộc để vào mạng",
  "guestData.consent.title": "Đồng ý nhận tiếp thị",
  "guestData.consent.body":
    "Một ô đánh dấu riêng, không bao giờ đánh dấu sẵn và không bao giờ bắt buộc để vào mạng. Chấp nhận điều khoản không phải là đồng ý. Foyer chỉ ghi nhận đồng ý khi khách đánh dấu ô — thời điểm, và đúng câu họ đã đọc — và khách có thể rút lại từ cổng đăng nhập bất cứ lúc nào.",
  "guestData.consent.enabled": "Hỏi khách có muốn nhận tin từ bạn không",
  "guestData.consent.text.vi": "Câu bên cạnh ô đánh dấu — tiếng Việt",
  "guestData.consent.text.en": "Câu bên cạnh ô đánh dấu — tiếng Anh",
  "guestData.consent.textHint":
    "Nói rõ ai sẽ liên hệ, bằng cách nào, và về việc gì. Sửa câu này sau không thay đổi những gì khách trước đã đồng ý: mỗi phiên bản đều được giữ.",
  "guestData.consent.suggest": "Dùng câu gợi ý",
  "guestData.consent.version": (lang: string, v: string) => `Phiên bản ${lang} hiện tại: ${v.slice(0, 12)}…`,
  "guestData.save": "Lưu",
  "guestData.saved": "Đã lưu. Cổng đăng nhập hỏi điều này từ trang tiếp theo khách mở.",
  "guestData.failed": "Cài đặt chưa được lưu",
  "guestData.list.title": "Các đồng ý đã nhận",
  "guestData.list.body":
    "Chỉ những khách đã đánh dấu ô mới có ở đây. Một đồng ý đã rút lại vẫn giữ thời điểm đồng ý và rút lại, và mất thông tin liên hệ của khách.",
  "guestData.list.empty": "Chưa có khách nào đồng ý.",
  "guestData.list.filter": "Hiện",
  "guestData.list.all": "Tất cả",
  "guestData.list.active": "Còn hiệu lực",
  "guestData.list.withdrawn": "Đã rút lại",
  "guestData.col.given": "Thời điểm đồng ý",
  "guestData.col.contact": "Liên hệ",
  "guestData.col.text": "Câu đã đồng ý",
  "guestData.col.state": "Trạng thái",
  "guestData.state.active": "Còn hiệu lực",
  "guestData.state.withdrawn": (when: string, by: string) => `Rút lại ${when} bởi ${by}`,
  "guestData.by.guest": "khách",
  "guestData.by.operator": "quản trị viên",
  "guestData.cleared": "Đã xoá khi rút lại",
  "guestData.more": "Xem thêm",
  "guestData.withdraw": "Rút lại…",
  "guestData.withdraw.title": "Rút lại đồng ý này?",
  "guestData.withdraw.verb": "Rút lại",
  "guestData.withdraw.consequence":
    "Họ tên, số điện thoại và địa chỉ e-mail của khách bị xoá khỏi bản ghi, và lần xuất tiếp theo ghi nó là đã rút lại.",
  "guestData.withdraw.recovery":
    "Không khôi phục được từ đây: chỉ khách mới đồng ý lại được, bằng cách đánh dấu ô. Nhật ký thao tác ghi lại rằng bạn đã làm việc này.",
  "guestData.withdrawn": "Đã rút lại đồng ý.",
  "guestData.list.failed": "Không tải được danh sách đồng ý",
  "guestData.export.title": "Xuất danh sách đồng ý",
  "guestData.export.body": (days: number) =>
    `Tệp CSV gồm mọi đồng ý được đưa ra hoặc rút lại trong khoảng ngày bạn chọn, tối đa ${days} ngày. Nạp các dòng đã rút lại vào công cụ gửi thư của bạn như danh sách huỷ đăng ký. Mọi lần xuất đều được ghi vào nhật ký thao tác.`,
  "guestData.export.from": "Từ",
  "guestData.export.to": "Đến",
  "guestData.export.go": "Xuất danh sách đồng ý",
  "guestData.export.failed": "Không xuất được",
};
