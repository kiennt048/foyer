/** `/operators` in Vietnamese. A total translation of `../operators.ts`. */
import type { Translation } from "../catalogue.js";
import type { operators } from "../operators.js";

export const vi: Translation<typeof operators> = {
  "operators.add": "Thêm người vận hành",
  "operators.search": "Tìm tên đăng nhập…",
  "operators.searchLabel": "Tìm người vận hành",
  "operators.empty.title": "Chưa có người vận hành.",
  "operators.empty.description": "Không ai đăng nhập được vào bảng điều khiển này.",
  "operators.col.username": "Tên đăng nhập",
  "operators.col.role": "Vai trò",
  "operators.col.status": "Trạng thái",
  "operators.col.created": "Ngày tạo",
  "operators.col.lastLogin": "Lần đăng nhập cuối",
  "operators.noDisplayName": "không có tên hiển thị",
  "operators.enabled": "Đang hoạt động",
  "operators.disabled": "Đã vô hiệu hoá",
  "operators.neverSignedIn": "Chưa từng đăng nhập.",
  "operators.action.disable": "Vô hiệu hoá",
  "operators.action.enable": "Kích hoạt",
  "operators.action.disableWhy":
    "Người vận hành bị vô hiệu hoá không đăng nhập được, và mọi bảng điều khiển họ đang mở bị đăng xuất ngay.",
  "operators.action.enableWhy": "Người vận hành này đăng nhập lại được từ lần thử tiếp theo.",
  "operators.action.makeAdministrator": "Đặt làm quản trị viên",
  "operators.action.makeStaff": "Đặt làm nhân viên",
  "operators.action.roleWhy":
    "Nhân viên phát phiếu và tra cứu khách. Quản trị viên còn cấu hình mạng — điểm truy cập, khoá bí mật dùng chung, vùng truy cập tự do và cổng đăng nhập.",
  "operators.toast.enabled": (username: string) => `Đã kích hoạt ${username}.`,
  "operators.toast.disabled": (username: string) => `Đã vô hiệu hoá ${username}.`,
  "operators.toast.role": (username: string, role: string) => `${username} giờ là ${role}.`,
  "operators.lastAdmin.title": (username: string) => `${username} là quản trị viên đang hoạt động duy nhất`,
  "operators.lastAdmin.body":
    "Vô hiệu hoá, hạ vai trò hoặc xoá tài khoản này sẽ để lại một bản cài đặt không ai quản trị được, và không có cách đặt lại mật khẩu để khôi phục. Hãy cấp vai trò quản trị viên cho người vận hành khác trước — ba nút này sẽ xuất hiện lại ngay khi có người khác giữ vai trò đó.",
  "operators.lastAdmin.why": "Đây là quản trị viên đang hoạt động duy nhất.",
  "operators.noReset.title": "Không có chức năng đặt lại mật khẩu",
  "operators.noReset.body":
    "Không ai đặt được mật khẩu cho người vận hành khác. Đặt lại mật khẩu cần một kênh gửi — máy gửi e-mail hoặc đường dẫn dùng một lần — và sản phẩm này không có cả hai, còn đọc mật khẩu mới qua bất kỳ kênh nào có sẵn thì tệ hơn cách còn lại. Nếu ai đó bị khoá ngoài, hãy vô hiệu hoá tài khoản của họ và tạo tài khoản khác.",
  "operators.delete.title": "Xoá người vận hành",
  "operators.delete.verb": "Xoá người vận hành",
  "operators.delete.consequence": (username: string) =>
    `${username} không đăng nhập được nữa, và mọi bảng điều khiển họ đang mở bị đăng xuất ngay.`,
  "operators.delete.recovery":
    "Các mục của họ trong nhật ký thao tác vẫn còn, và vẫn mang tên đăng nhập của họ. Vô hiệu hoá là cách có thể đảo ngược: người vận hành có thể quay lại thì nên vô hiệu hoá thay vì xoá.",
  "operators.create.title": "Thêm người vận hành",
  "operators.create.failed.title": "Chưa tạo được người vận hành",
  "operators.create.username": "Tên đăng nhập",
  "operators.create.usernameHint":
    "Không đổi được về sau. Nhật ký thao tác ghi tên này tại thời điểm mỗi thay đổi, nên đổi tên sẽ khiến một người mang hai tên.",
  "operators.create.displayName": "Tên hiển thị",
  "operators.create.role": "Vai trò",
  "operators.create.roleHint":
    "Nhân viên phát phiếu, tra cứu khách và quản lý tài khoản khách. Họ xem được cấu hình mạng nhưng không thay đổi được gì.",
  "operators.create.password": "Mật khẩu ban đầu",
  "operators.create.passwordHint": (min: number) =>
    `Ít nhất ${min} ký tự. Hãy trao tận tay — đó là kênh gửi duy nhất, và sau đó không ai đặt lại được.`,
  "operators.create.submit": "Tạo người vận hành",
};
