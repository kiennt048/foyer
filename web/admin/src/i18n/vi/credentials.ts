/** The account dialogs in Vietnamese. A total translation of `../credentials.ts`. */
import type { Translation } from "../catalogue.js";
import type { credentials } from "../credentials.js";

export const vi: Translation<typeof credentials> = {
  "accounts.create.failed.title": "Chưa tạo được tài khoản",
  "accounts.create.title": "Thêm tài khoản",
  "accounts.create.username": "Tên đăng nhập",
  "accounts.create.displayName": "Tên hiển thị",
  "accounts.create.password": "Mật khẩu",
  "accounts.create.passwordHint":
    "Lưu bằng argon2id. Tài khoản này không dùng được với PEAP-MSCHAPv2 — phương thức đó buộc máy chủ giữ mã băm NT, và được bật riêng.",
  "accounts.create.submit": "Tạo tài khoản",
  "credentials.dot1x": "Cho phép cả 802.1X (EAP-TTLS/PAP)",
  "credentials.dot1xHint":
    "Lưu thêm một bộ kiểm tra bcrypt mà máy chủ RADIUS kiểm tra được, tạo từ mật khẩu ở trên. Nó là một chiều, nhưng không phải argon2id. Mật khẩu tối đa 72 byte; tên đăng nhập 802.1X phải là duy nhất trên mọi địa điểm.",
  "credentials.title": (username: string) => `Thông tin đăng nhập của ${username}`,
  "credentials.password": "Mật khẩu mới",
  "credentials.passwordHint": "Để trống để giữ mật khẩu hiện tại. Bật 802.1X thì cần nhập.",
  "credentials.submit": "Lưu thông tin đăng nhập",
  "credentials.failed": "Thông tin đăng nhập chưa được thay đổi",
  "credentials.saved": (username: string) => `Đã lưu thông tin đăng nhập của ${username}.`,
};
