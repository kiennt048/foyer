/** The first-access setup in Vietnamese. A total translation of `../setup.ts`. */
import type { Translation } from "../catalogue.js";
import type { setup } from "../setup.js";

export const vi: Translation<typeof setup> = {
  "setup.title": "Tạo quản trị viên",
  "setup.lede":
    "Foyer không có mật khẩu mặc định. Nhập mã thiết lập mà trình cài đặt đã in ra, rồi chọn tên đăng nhập và mật khẩu cho quản trị viên.",
  "setup.code": "Mã thiết lập",
  "setup.codeHint": "Do trình cài đặt in ra. Làm mất? Chạy lại trình cài đặt để lấy mã mới.",
  "setup.username": "Tên đăng nhập",
  "setup.password": "Mật khẩu",
  "setup.passwordRule": (min: number) => `Ít nhất ${min} ký tự.`,
  "setup.submit": "Tạo quản trị viên",
  "setup.failed": "Chưa tạo được quản trị viên",
  "setup.once": "Việc này chỉ làm được một lần. Sau đó bảng điều khiển chỉ còn màn hình đăng nhập.",
  "setup.unready.title": "Quá trình cài đặt chưa hoàn tất",
  "setup.unready.lede":
    "Foyer chưa có tài khoản quản trị viên nào, và chưa có mã thiết lập nào được mở, nên không có gì để đăng nhập.",
  "setup.unready.fix":
    "Trình cài đặt chưa chạy tới bước tạo tài khoản. Hãy chạy lại trình cài đặt trên máy chủ: nó sẽ mở phần thiết lập và in ra một mã dùng một lần. Quay lại với mã đó và màn hình này sẽ trở thành biểu mẫu tạo quản trị viên.",
  "setup.unready.noCode":
    "Không thể tạo quản trị viên từ trang này. Foyer không có mật khẩu mặc định và sẽ không tự tạo cho người mở trang đầu tiên.",
  "setup.unready.guard": "Không có lối vào từ trang này",
};
