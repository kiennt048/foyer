/** `/attempts` in Vietnamese. A total translation of `../attempts.ts`. */
import type { Translation } from "../catalogue.js";
import type { attempts } from "../attempts.js";

export const vi: Translation<typeof attempts> = {
  "attempts.search": "Tìm MAC, tên đăng nhập, điểm truy cập…",
  "attempts.searchLabel": "Tìm lượt đăng nhập",
  "attempts.filter.rejected": "Bị từ chối",
  "attempts.filter.accepted": "Được chấp nhận",
  "attempts.filter.source.radius": "Từ RADIUS",
  "attempts.filter.source.portal": "Từ cổng đăng nhập",
  "attempts.filter.source.driver": "Từ điểm truy cập",
  "attempts.col.time": "Thời điểm",
  "attempts.col.outcome": "Kết quả",
  "attempts.col.what": "Chuyện gì đã xảy ra",
  "attempts.col.mac": "Địa chỉ MAC",
  "attempts.col.identity": "Danh tính",
  "attempts.col.nas": "Điểm truy cập",
  "attempts.outcome.accepted": "Được chấp nhận",
  "attempts.outcome.rejected": "Bị từ chối",
  "attempts.source.radius": "RADIUS",
  "attempts.source.portal": "Cổng đăng nhập",
  "attempts.source.driver": "Điểm truy cập",
  "attempts.kind.radius-accept": "RADIUS chấp nhận",
  "attempts.kind.radius-reject": "RADIUS từ chối",
  "attempts.kind.voucher-unknown": "Không có mã phiếu này",
  "attempts.kind.voucher-expired": "Phiếu đã hết hạn",
  "attempts.kind.voucher-exhausted": "Phiếu đã dùng hết",
  "attempts.kind.voucher-revoked": "Phiếu đã bị thu hồi",
  "attempts.kind.account-unknown": "Không có tài khoản này",
  "attempts.kind.account-disabled": "Tài khoản đã bị vô hiệu hoá",
  "attempts.kind.account-bad-password": "Sai mật khẩu",
  "attempts.kind.rate-limited": "Quá nhiều lần thử",
  "attempts.kind.presence-unverified": "Thiết bị không có trên mạng",
  "attempts.kind.authorize-failed": "Điểm truy cập từ chối cấp quyền",
  "attempts.kind.grant-created": "Đã đăng nhập",
  "attempts.why.radius-reject":
    "RADIUS ghi lại rằng một yêu cầu bị từ chối nhưng không bao giờ ghi lý do — lý do được quyết định bên trong quá trình xác thực và không cột nào lưu nó.",
  "attempts.why.presence-unverified":
    "Cổng đăng nhập được gọi với một địa chỉ MAC không gắn với điểm truy cập nào. Đây là dấu hiệu của một đường dẫn cổng đăng nhập bị giả mạo, không phải khách nhầm lẫn.",
  "attempts.why.authorize-failed":
    "Khách đã qua mọi bước kiểm tra của Foyer. Điểm truy cập hoặc bộ điều khiển của nó từ chối lệnh cho khách vào mạng, nên màn hình phiên sẽ không hiện gì về việc này.",
  "attempts.why.rate-limited": "Thông tin đăng nhập không có vấn đề gì. Lần thử bị từ chối vì đến quá nhanh.",
  "attempts.noMac": "Không được ghi",
  "attempts.noMacWhy": "Lần thử này đến Foyer mà không có địa chỉ MAC, nên tìm theo MAC sẽ không bao giờ thấy nó.",
  "attempts.noNas": "Không khớp",
  "attempts.noNasWhy":
    "Yêu cầu đến từ một địa chỉ không khớp điểm truy cập nào đã đăng ký. Với hệ thống dùng bộ điều khiển, đây là dấu hiệu của một điểm truy cập vừa được thêm mà chưa đăng ký.",
  "attempts.coverage.notRecorded.title": "Địa chỉ MAC đang không được ghi lại.",
  "attempts.coverage.notRecorded.body":
    "Tìm theo MAC sẽ không thấy gì ở đây, dù khách đã làm gì. Cấu hình FreeRADIUS được tạo ra không điền cột địa chỉ thiết bị.",
  "attempts.coverage.unknown.title": "Chưa xác định được địa chỉ MAC có được ghi lại hay không.",
  "attempts.coverage.unknown.body":
    "Kết quả tìm kiếm trống có thể nghĩa là khách chưa từng thử, hoặc có thể là cột đó không bao giờ được ghi.",
  "attempts.empty.title": "Chưa có lượt nào.",
  "attempts.empty.description": "Các lượt hiện ra khi khách thử đăng nhập.",
  "attempts.blind.title": "Những gì màn hình này không thấy",
  "attempts.blind.lede":
    "Những trường hợp này hoàn toàn không tạo dòng nào ở đây. Nếu một khách không vào được mạng và không có lượt nào của họ, thì một trong các lý do này là khả năng cao nhất.",
  "attempts.blind.shared-secret-mismatch":
    "Khoá bí mật dùng chung không khớp. FreeRADIUS bỏ yêu cầu trước khi ghi bất cứ thứ gì, nên cả một địa điểm có thể mất mạng mà màn hình này không hiện gì.",
  "attempts.blind.unknown-radius-client":
    "Yêu cầu đến từ một địa chỉ không có trong danh sách thiết bị. Thường gặp ngay sau khi một điểm truy cập được thêm vào bộ điều khiển.",
  "attempts.blind.never-associated":
    "Khách chưa bao giờ tới được điểm truy cập — ngoài vùng phủ sóng, sai mạng, hoặc bộ phát sóng đang tắt.",
  "attempts.blind.no-radius-configured":
    "Địa điểm này chưa cấu hình thiết bị RADIUS nào, nên không có gì ghi vào luồng này.",
  "attempts.blind.no-radius-configured.noDevices":
    "Địa điểm này chưa đăng ký điểm truy cập nào, nên không có gì gửi yêu cầu xác thực. Đăng ký một điểm truy cập và các lượt sẽ hiện ra ở đây khi khách thử đăng nhập.",
  "attempts.blind.no-radius-configured.noSecret":
    "Mọi điểm truy cập đã đăng ký ở địa điểm này đều không có khoá bí mật RADIUS dùng chung, nên không có gì tới được FreeRADIUS. Với hệ thống cấp quyền qua API của bộ điều khiển, đó là thiết kế; với mọi hệ thống khác, đó là lý do không ai vào được mạng.",
  "attempts.blind.before-retention": (days: number) =>
    `Cũ hơn ${days} ngày. Các lượt đăng nhập không được giữ mãi.`,
};
