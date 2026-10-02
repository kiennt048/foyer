/** `/password` in Vietnamese. A total translation of `../password.ts`. */
import type { Translation } from "../catalogue.js";
import type { password } from "../password.js";

export const vi: Translation<typeof password> = {
  "password.current": "Mật khẩu hiện tại",
  "password.currentHint":
    "Phiên làm việc chứng minh trình duyệt này đã đăng nhập. Nó không chứng minh bạn là người đang ngồi trước bàn phím.",
  "password.new": "Mật khẩu mới",
  "password.rule": (min: number) =>
    `Ít nhất ${min} ký tự. Độ dài quan trọng hơn ký tự đặc biệt — một cụm từ gồm các từ thông thường mạnh hơn một mật khẩu ngắn có ký hiệu.`,
  "password.submit": "Đổi mật khẩu",
  "password.done": "Đã đổi mật khẩu.",
  "password.failed": "Chưa đổi được mật khẩu",
  "password.noReset":
    "Không ai đặt lại được mật khẩu này cho bạn. Không có máy gửi e-mail và không có đường dẫn dùng một lần, nên quản trị viên muốn giúp bạn vào lại sẽ phải vô hiệu hoá tài khoản này và tạo tài khoản khác.",
  "password.signedOut.title": "Chưa đăng nhập.",
  "password.signedOut.body": "Chưa có mật khẩu nào để đổi cho tới khi bạn đăng nhập.",
};
