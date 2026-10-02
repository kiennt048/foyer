/** `/privacy` in Vietnamese. A total translation of `../privacy.ts`. */
import type { Translation } from "../catalogue.js";
import type { privacy } from "../privacy.js";

export const vi: Translation<typeof privacy> = {
  "privacy.retention.title": "Giữ trong bao lâu",
  "privacy.retention.body":
    "Foyer xoá từng loại dữ liệu khách khi nó cũ hơn thời hạn của loại đó, trong một lượt quét chạy vài phút một lần. Thời hạn bạn rút ngắn có hiệu lực ở lượt quét tiếp theo, và những gì bị xoá không lấy lại được.",
  "privacy.retention.days": "Ngày",
  "privacy.retention.keep": "0 nghĩa là giữ cho tới khi bạn xoá.",
  "privacy.retention.bounds": (min: number, max: number) => `Từ ${min} đến ${max} ngày.`,
  "privacy.retention.default": (days: number) =>
    days === 0 ? "Mặc định: giữ cho tới khi bị xoá." : `Mặc định: ${days} ngày.`,
  "privacy.retention.tables": "Bảng",
  "privacy.retention.neverSwept": "Chưa quét lần nào.",
  "privacy.retention.swept": (when: string, removed: number) => `Quét lần cuối ${when}; đã xoá ${removed} dòng.`,
  "privacy.retention.sweepFailed": (when: string) =>
    `Lượt quét gần nhất, ${when}, thất bại giữa chừng. Lý do có trong nhật ký của máy chủ điều khiển.`,
  "privacy.retention.restore": "Khôi phục mặc định",
  "privacy.retention.save": "Lưu thời hạn",
  "privacy.retention.saved": "Đã lưu thời hạn lưu giữ.",
  "privacy.retention.failed": "Chưa lưu được thời hạn",
  "privacy.retention.unchanged": "Không có gì thay đổi.",
  "privacy.category.accounting": "Lịch sử phiên",
  "privacy.category.accounting.what":
    "Từng phiên của khách — thiết bị, điểm truy cập, địa chỉ, dung lượng đã dùng. Tính theo lúc phiên kết thúc; phiên đang mở không bao giờ bị xoá.",
  "privacy.category.authAttempts": "Lượt đăng nhập",
  "privacy.category.authAttempts.what": "Mọi lần đăng nhập được chấp nhận và bị từ chối, từ mạng và từ cổng đăng nhập.",
  "privacy.category.portalGrants": "Quyền truy cập qua cổng đăng nhập",
  "privacy.category.portalGrants.what": "Những gì cổng đăng nhập cho phép từng thiết bị, tính theo lúc kết thúc.",
  "privacy.category.audit": "Nhật ký thao tác",
  "privacy.category.audit.what": "Các thay đổi của người vận hành. Giữ lâu hơn dữ liệu khách, và không bao giờ bị xoá từng mục.",
  "privacy.category.guestAccounts": "Tài khoản khách",
  "privacy.category.guestAccounts.what": "Các tài khoản bạn đã tạo, bị xoá khi không được dùng trong suốt thời hạn.",
  "privacy.category.marketingConsents": "Đồng ý nhận tiếp thị",
  "privacy.category.marketingConsents.what":
    "Các đồng ý khách đã đánh dấu trên cổng đăng nhập, tính theo lúc rút lại, hoặc lúc đồng ý với đồng ý còn hiệu lực.",
  "privacy.erase.title": "Xoá dữ liệu của một người",
  "privacy.erase.body":
    "Khi khách yêu cầu được xoá, hãy xoá mọi bản ghi Foyer giữ về họ. Hỏi khách địa chỉ MAC mà điện thoại của họ hiển thị cho mạng này — điện thoại dùng một địa chỉ riêng khác nhau cho mỗi mạng.",
  "privacy.erase.kind": "Xác định họ bằng",
  "privacy.erase.kind.mac": "Địa chỉ MAC của thiết bị",
  "privacy.erase.kind.account": "Tài khoản khách",
  "privacy.erase.kind.phone": "Số điện thoại",
  "privacy.erase.kind.email": "Địa chỉ e-mail",
  "privacy.erase.mac": "Địa chỉ MAC",
  "privacy.erase.macHint": "Viết kiểu nào cũng được: AA-BB-CC-00-11-22, aa:bb:cc:00:11:22, aabb.cc00.1122.",
  "privacy.erase.account": "Mã tài khoản",
  "privacy.erase.accountHint": "Lấy từ màn hình Tài khoản.",
  "privacy.erase.phone": "Số điện thoại",
  "privacy.erase.phoneHint":
    "Viết kiểu nào cũng được: 0912 345 678, +84 912 345 678. Xoá các mã đã gửi tới số này, các lần đăng nhập nó đã được xác minh, và mọi biểu mẫu đăng nhập đã khai số này, ở mọi địa điểm.",
  "privacy.erase.email": "Địa chỉ e-mail",
  "privacy.erase.emailHint":
    "Địa chỉ họ đã khai trên biểu mẫu đăng nhập hoặc khi đồng ý nhận tin tiếp thị. Chữ hoa hay chữ thường đều được.",
  "privacy.erase.submit": "Xoá…",
  "privacy.erase.confirmTitle": "Xoá dữ liệu của người này?",
  "privacy.erase.confirmVerb": "Xoá",
  "privacy.erase.consequence":
    "Mọi phiên, lượt đăng nhập, quyền truy cập và phiên cổng đăng nhập có tên họ đều bị xoá, và với tài khoản thì xoá cả tài khoản. Không thể hoàn tác.",
  "privacy.erase.recovery":
    "Nhật ký thao tác ghi lại rằng bạn đã làm việc này, với địa chỉ được che; các mục nhật ký trước đó về người này vẫn giữ sự kiện nhưng địa chỉ được thay bằng bí danh. Nếu không ghi được nhật ký thì không có gì bị xoá. Bản sao lưu và lịch sử riêng của bộ điều khiển không bị ảnh hưởng.",
  "privacy.erase.failed": "Không có gì được báo là đã xoá",
  "privacy.erase.done": (masked: string, total: number) => `${masked}: đã xoá ${total} bản ghi.`,
  "privacy.erase.table": "Bảng",
  "privacy.erase.rows": "Đã xoá",
  "privacy.erase.pseudonymised": (n: number) => `Giữ ${n} mục nhật ký thao tác, với định danh được thay bằng bí danh.`,
};
