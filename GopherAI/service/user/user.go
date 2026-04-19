package user

import (
	"log"

	"GopherAI/common/code"
	myemail "GopherAI/common/email"
	myredis "GopherAI/common/redis"
	"GopherAI/dao/user"
	"GopherAI/model"
	"GopherAI/utils"
	"GopherAI/utils/myjwt"
)

// enableRegisterCaptcha 为 false 时跳过邮箱验证码校验（临时关闭，恢复注册验证码时改为 true）
const enableRegisterCaptcha = false

// enableRegisterWelcomeEmail 为 true 时，注册成功后会通过 SMTP 把随机账号发到用户邮箱。
// 关闭验证码期间若 QQ 邮箱未开通 SMTP / 授权码不对会报 535，此前会导致注册仍返回失败——本地开发请保持 false。
const enableRegisterWelcomeEmail = false

func Login(username, password string) (string, code.Code) {
	var userInformation *model.User
	var ok bool
	//1:判断用户是否存在
	if ok, userInformation = user.IsExistUser(username); !ok {

		return "", code.CodeUserNotExist
	}
	//2:判断用户是否密码账号正确
	if userInformation.Password != utils.MD5(password) {
		return "", code.CodeInvalidPassword
	}
	//3:返回一个Token
	token, err := myjwt.GenerateToken(userInformation.ID, userInformation.Username)

	if err != nil {
		return "", code.CodeServerBusy
	}
	return token, code.CodeSuccess
}

func Register(email, password, captcha string) (string, code.Code) {

	var ok bool
	var userInformation *model.User

	//1:先判断该邮箱是否已注册（用户名是随机数字，必须用 email 查重）
	if ok, _ := user.IsExistEmail(email); ok {
		return "", code.CodeUserExist
	}

	//2:从redis中验证验证码是否有效（可通过 enableRegisterCaptcha 临时关闭）
	if enableRegisterCaptcha {
		if ok, _ := myredis.CheckCaptchaForEmail(email, captcha); !ok {
			return "", code.CodeInvalidCaptcha
		}
	}

	//3：生成11位的账号
	username := utils.GetRandomNumbers(11)

	//4：注册到数据库中
	if userInformation, ok = user.Register(username, email, password); !ok {
		return "", code.CodeServerBusy
	}

	//5：可选：把随机账号发到邮箱（与验证码无关；失败不应挡注册）
	if enableRegisterWelcomeEmail {
		if err := myemail.SendCaptcha(email, username, user.UserNameMsg); err != nil {
			log.Printf("[register] welcome email send failed (user still created): %v", err)
		}
	}

	// 6:生成Token
	token, err := myjwt.GenerateToken(userInformation.ID, userInformation.Username)

	if err != nil {
		return "", code.CodeServerBusy
	}

	return token, code.CodeSuccess
}

// 往指定邮箱发送验证码
// 分为以下任务：
// 1：先存放redis
// 2：再进行远程发送
func SendCaptcha(email_ string) code.Code {
	send_code := utils.GetRandomNumbers(6)
	//1:先存放到redis
	if err := myredis.SetCaptchaForEmail(email_, send_code); err != nil {
		return code.CodeServerBusy
	}

	//2:再进行远程发送
	if err := myemail.SendCaptcha(email_, send_code, myemail.CodeMsg); err != nil {
		return code.CodeServerBusy
	}

	return code.CodeSuccess
}
