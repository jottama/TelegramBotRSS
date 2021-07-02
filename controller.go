package bot

import (
	"bytes"
	"fmt"
	"go.uber.org/zap"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/indes/flowerss-bot/bot/fsm"
	"github.com/indes/flowerss-bot/config"
	"github.com/indes/flowerss-bot/model"

	tb "gopkg.in/tucnak/telebot.v2"
)

var (
	feedSettingTmpl = `
Suscripcion<b>Configurar</b>
[id] {{ .sub.ID }}
[Titulo] {{ .source.Title }}
[Link] {{.source.Link }}
[Obtener actualizaciones] {{if ge .source.ErrorCount .Count }}se acab0 el tiempo{{else if lt .source.ErrorCount .Count }}Cerrar{{end}}
[Frecuencia de rastreo] {{ .sub.Interval }}minuto
[!] {{if eq .sub.EnableNotification 0}}Cerrar{{else if eq .sub.EnableNotification 1}}Enceneder{{end}}
[Telegraph] {{if eq .sub.EnableTelegraph 0}}Cerrar{{else if eq .sub.EnableTelegraph 1}}Enceneder{{end}}
[Tag] {{if .sub.Tag}}{{ .sub.Tag }}{{else}}No{{end}}
`
)

func toggleCtrlButtons(c *tb.Callback, action string) {

	if (c.Message.Chat.Type == tb.ChatGroup || c.Message.Chat.Type == tb.ChatSuperGroup) &&
		!userIsAdminOfGroup(c.Sender.ID, c.Message.Chat) {
		// check admin
		return
	}

	data := strings.Split(c.Data, ":")
	subscriberID, _ := strconv.Atoi(data[0])
	if subscriberID != c.Sender.ID {
		channelChat, err := B.ChatByID(fmt.Sprintf("%d", subscriberID))

		if err != nil {
			return
		}

		if !UserIsAdminChannel(c.Sender.ID, channelChat) {
			return
		}
	}

	msg := strings.Split(c.Message.Text, "\n")
	subID, err := strconv.Atoi(strings.Split(msg[1], " ")[1])
	if err != nil {
		_ = B.Respond(c, &tb.CallbackResponse{
			Text: "error",
		})
		return
	}
	sub, err := model.GetSubscribeByID(subID)
	if sub == nil || err != nil {
		_ = B.Respond(c, &tb.CallbackResponse{
			Text: "error",
		})
		return
	}

	source, _ := model.GetSourceById(sub.SourceID)
	t := template.New("setting template")
	_, _ = t.Parse(feedSettingTmpl)

	switch action {
	case "toggleNotice":
		err = sub.ToggleNotification()
	case "toggleTelegraph":
		err = sub.ToggleTelegraph()
	case "toggleUpdate":
		err = source.ToggleEnabled()
	}

	if err != nil {
		_ = B.Respond(c, &tb.CallbackResponse{
			Text: "error",
		})
		return
	}

	sub.Save()

	text := new(bytes.Buffer)

	_ = t.Execute(text, map[string]interface{}{"source": source, "sub": sub, "Count": config.ErrorThreshold})
	_ = B.Respond(c, &tb.CallbackResponse{
		Text: "Modificado con éxito",
	})
	_, _ = B.Edit(c.Message, text.String(), &tb.SendOptions{
		ParseMode: tb.ModeHTML,
	}, &tb.ReplyMarkup{
		InlineKeyboard: genFeedSetBtn(c, sub, source),
	})
}

func startCmdCtr(m *tb.Message) {
	user, _ := model.FindOrCreateUserByTelegramID(m.Chat.ID)
	zap.S().Infof("/start user_id: %d telegram_id: %d", user.ID, user.TelegramID)
	_, _ = B.Send(m.Chat, fmt.Sprintf("Hola, bienvenido a flowerss. "))
}

func subCmdCtr(m *tb.Message) {

	url, mention := GetURLAndMentionFromMessage(m)

	if mention == "" {
		if url != "" {
			registFeed(m.Chat, url)
		} else {
			_, err := B.Send(m.Chat, "Responda a la URL de RSS ", &tb.ReplyMarkup{ForceReply: true})
			if err == nil {
				UserState[m.Chat.ID] = fsm.Sub
			}
		}
	} else {
		if url != "" {
			FeedForChannelRegister(m, url, mention)
		} else {
			_, _ = B.Send(m.Chat, "Para suscribirse al canal, utilice el comando '/ sub @ChannelID URL'")
		}
	}

}

func exportCmdCtr(m *tb.Message) {

	mention := GetMentionFromMessage(m)
	var sourceList []model.Source
	var err error
	if mention == "" {

		sourceList, err = model.GetSourcesByUserID(m.Chat.ID)
		if err != nil {
			zap.S().Warnf(err.Error())
			_, _ = B.Send(m.Chat, fmt.Sprintf("Exportación fallida"))
			return
		}
	} else {
		channelChat, err := B.ChatByID(mention)

		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}

		adminList, err := B.AdminsOf(channelChat)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}

		senderIsAdmin := false
		for _, admin := range adminList {
			if m.Sender.ID == admin.User.ID {
				senderIsAdmin = true
			}
		}

		if !senderIsAdmin {
			_, _ = B.Send(m.Chat, fmt.Sprintf("Los administradores que no son de canal no pueden realizar esta operación"))
			return
		}

		sourceList, err = model.GetSourcesByUserID(channelChat.ID)
		if err != nil {
			zap.S().Errorf(err.Error())
			_, _ = B.Send(m.Chat, fmt.Sprintf("Exportación fallida"))
			return
		}
	}

	if len(sourceList) == 0 {
		_, _ = B.Send(m.Chat, fmt.Sprintf("La lista de suscripciones está vacía "))
		return
	}

	opmlStr, err := ToOPML(sourceList)

	if err != nil {
		_, _ = B.Send(m.Chat, fmt.Sprintf("Exportación fallida"))
		return
	}
	opmlFile := &tb.Document{File: tb.FromReader(strings.NewReader(opmlStr))}
	opmlFile.FileName = fmt.Sprintf("subscriptions_%d.opml", time.Now().Unix())
	_, err = B.Send(m.Chat, opmlFile)

	if err != nil {
		_, _ = B.Send(m.Chat, fmt.Sprintf("Exportación fallida"))
		zap.S().Errorf("send opml file failed, err:%+v", err)
	}

}

func listCmdCtr(m *tb.Message) {
	mention := GetMentionFromMessage(m)

	var rspMessage string
	if mention != "" {
		// channel feed list
		channelChat, err := B.ChatByID(mention)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}

		if !checkPermitOfChat(int64(m.Sender.ID), channelChat) {
			B.Send(m.Chat, fmt.Sprintf("Los administradores que no son de canal no pueden realizar esta operación"))
			return
		}

		user, err := model.FindOrCreateUserByTelegramID(channelChat.ID)
		if err != nil {
			B.Send(m.Chat, fmt.Sprintf("Lista de errores internos @1"))
			return
		}

		subSourceMap, err := user.GetSubSourceMap()
		if err != nil {
			B.Send(m.Chat, fmt.Sprintf("Lista de errores internos @2"))
			return
		}

		sources, _ := model.GetSourcesByUserID(channelChat.ID)
		rspMessage = fmt.Sprintf("Canal [%s] (https://t.me/%s) Lista de suscripción: \n", channelChat.Title, channelChat.Username)
		if len(sources) == 0 {
			rspMessage = fmt.Sprintf("La lista de suscripción del canal [%s] (https://t.me/%s) está vacía", channelChat.Title, channelChat.Username)
		} else {
			for sub, source := range subSourceMap {
				rspMessage = rspMessage + fmt.Sprintf("[[%d]] [%s](%s)\n", sub.ID, source.Title, source.Link)
			}
		}
	} else {
		// private chat or group
		if m.Chat.Type != tb.ChatPrivate && !checkPermitOfChat(int64(m.Sender.ID), m.Chat) {
			// 无权限
			return
		}

		user, err := model.FindOrCreateUserByTelegramID(m.Chat.ID)
		if err != nil {
			B.Send(m.Chat, fmt.Sprintf("Lista de errores internos @1"))
			return
		}

		subSourceMap, err := user.GetSubSourceMap()
		if err != nil {
			B.Send(m.Chat, fmt.Sprintf("Lista de errores internos @2"))
			return
		}

		rspMessage = "Lista de suscripción actual：\n"
		if len(subSourceMap) == 0 {
			rspMessage = "La lista de suscripciones está vacía"
		} else {
			for sub, source := range subSourceMap {
				rspMessage = rspMessage + fmt.Sprintf("[[%d]] [%s](%s)\n", sub.ID, source.Title, source.Link)
			}
		}
	}
	_, _ = B.Send(m.Chat, rspMessage, &tb.SendOptions{
		DisableWebPagePreview: true,
		ParseMode:             tb.ModeMarkdown,
	})
}

func checkCmdCtr(m *tb.Message) {
	mention := GetMentionFromMessage(m)
	if mention != "" {
		channelChat, err := B.ChatByID(mention)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}
		adminList, err := B.AdminsOf(channelChat)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}

		senderIsAdmin := false
		for _, admin := range adminList {
			if m.Sender.ID == admin.User.ID {
				senderIsAdmin = true
			}
		}

		if !senderIsAdmin {
			_, _ = B.Send(m.Chat, fmt.Sprintf("Los administradores que no son de canal no pueden realizar esta operación"))
			return
		}

		sources, _ := model.GetErrorSourcesByUserID(channelChat.ID)
		message := fmt.Sprintf("Lista del canal [%s] (https://t.me/%s) de suscripciones caducadas: \n", channelChat.Title, channelChat.Username)
		if len(sources) == 0 {
			message = fmt.Sprintf("Canal [%s] (https://t.me/%s) Todas las suscripciones son normales", channelChat.Title, channelChat.Username)
		} else {
			for _, source := range sources {
				message = message + fmt.Sprintf("[[%d]] [%s](%s)\n", source.ID, source.Title, source.Link)
			}
		}

		_, _ = B.Send(m.Chat, message, &tb.SendOptions{
			DisableWebPagePreview: true,
			ParseMode:             tb.ModeMarkdown,
		})

	} else {
		sources, _ := model.GetErrorSourcesByUserID(m.Chat.ID)
		message := "Lista de suscripciones caducadas：\n"
		if len(sources) == 0 {
			message = "Todas las suscripciones son normales "
		} else {
			for _, source := range sources {
				message = message + fmt.Sprintf("[[%d]] [%s](%s)\n", source.ID, source.Title, source.Link)
			}
		}
		_, _ = B.Send(m.Chat, message, &tb.SendOptions{
			DisableWebPagePreview: true,
			ParseMode:             tb.ModeMarkdown,
		})
	}

}

func setCmdCtr(m *tb.Message) {

	mention := GetMentionFromMessage(m)
	var sources []model.Source
	var ownerID int64
	// 获取订阅列表
	if mention == "" {
		sources, _ = model.GetSourcesByUserID(m.Chat.ID)
		ownerID = int64(m.Chat.ID)
		if len(sources) <= 0 {
			_, _ = B.Send(m.Chat, "Actualmente no hay feeds")
			return
		}

	} else {

		channelChat, err := B.ChatByID(mention)

		if err != nil {
			_, _ = B.Send(m.Chat, "Error al obtener la información del canal.")
			return
		}

		if UserIsAdminChannel(m.Sender.ID, channelChat) {
			sources, _ = model.GetSourcesByUserID(channelChat.ID)

			if len(sources) <= 0 {
				_, _ = B.Send(m.Chat, "Channel no tiene feeds.")
				return
			}
			ownerID = channelChat.ID

		} else {
			_, _ = B.Send(m.Chat, "Los administradores que no pertenecen al canal no pueden realizar esta operación.")
			return
		}

	}

	var replyButton []tb.ReplyButton
	replyKeys := [][]tb.ReplyButton{}
	setFeedItemBtns := [][]tb.InlineButton{}

	// 配置按钮
	for _, source := range sources {
		// 添加按钮
		text := fmt.Sprintf("%s %s", source.Title, source.Link)
		replyButton = []tb.ReplyButton{
			tb.ReplyButton{Text: text},
		}
		replyKeys = append(replyKeys, replyButton)

		setFeedItemBtns = append(setFeedItemBtns, []tb.InlineButton{
			tb.InlineButton{
				Unique: "set_feed_item_btn",
				Text:   fmt.Sprintf("[%d] %s", source.ID, source.Title),
				Data:   fmt.Sprintf("%d:%d", ownerID, source.ID),
			},
		})
	}

	_, _ = B.Send(m.Chat, "Seleccione la fuente que desea configurar", &tb.ReplyMarkup{
		InlineKeyboard: setFeedItemBtns,
	})
}

func setFeedItemBtnCtr(c *tb.Callback) {

	if (c.Message.Chat.Type == tb.ChatGroup || c.Message.Chat.Type == tb.ChatSuperGroup) &&
		!userIsAdminOfGroup(c.Sender.ID, c.Message.Chat) {
		return
	}

	data := strings.Split(c.Data, ":")
	subscriberID, _ := strconv.Atoi(data[0])

	// 如果订阅者与按钮点击者id不一致，需要验证管理员权限

	if subscriberID != c.Sender.ID {
		channelChat, err := B.ChatByID(fmt.Sprintf("%d", subscriberID))

		if err != nil {
			return
		}

		if !UserIsAdminChannel(c.Sender.ID, channelChat) {
			return
		}
	}

	sourceID, _ := strconv.Atoi(data[1])

	source, err := model.GetSourceById(uint(sourceID))

	if err != nil {
		_, _ = B.Edit(c.Message, "No se pudo encontrar el feed, código de error 01.")
		return
	}

	sub, err := model.GetSubscribeByUserIDAndSourceID(int64(subscriberID), source.ID)
	if err != nil {
		_, _ = B.Edit(c.Message, "El usuario no se ha suscrito al rss, código de error 02.")
		return
	}

	t := template.New("setting template")
	_, _ = t.Parse(feedSettingTmpl)
	text := new(bytes.Buffer)
	_ = t.Execute(text, map[string]interface{}{"source": source, "sub": sub, "Count": config.ErrorThreshold})

	_, _ = B.Edit(
		c.Message,
		text.String(),
		&tb.SendOptions{
			ParseMode: tb.ModeHTML,
		}, &tb.ReplyMarkup{
			InlineKeyboard: genFeedSetBtn(c, sub, source),
		},
	)
}

func setSubTagBtnCtr(c *tb.Callback) {

	// 权限验证
	if !feedSetAuth(c) {
		return
	}
	data := strings.Split(c.Data, ":")
	ownID, _ := strconv.Atoi(data[0])
	sourceID, _ := strconv.Atoi(data[1])

	sub, err := model.GetSubscribeByUserIDAndSourceID(int64(ownID), uint(sourceID))
	if err != nil {
		_, _ = B.Send(
			c.Message.Chat,
			"error del sistema，Código 04",
		)
		return
	}
	msg := fmt.Sprintf(
		"Utilice el comando `/setfeedtag %d tags` para establecer etiquetas para esta suscripción. Las etiquetas son las etiquetas que se deben establecer, separadas por espacios. (Se pueden configurar hasta tres etiquetas) \n"+
			"Por ejemplo: `/setfeedtag %d technology apple`",
		sub.ID, sub.ID)

	_ = B.Delete(c.Message)

	_, _ = B.Send(
		c.Message.Chat,
		msg,
		&tb.SendOptions{ParseMode: tb.ModeMarkdown},
	)
}

func genFeedSetBtn(c *tb.Callback, sub *model.Subscribe, source *model.Source) [][]tb.InlineButton {
	setSubTagKey := tb.InlineButton{
		Unique: "set_set_sub_tag_btn",
		Text:   "Configuración de etiquetas",
		Data:   c.Data,
	}

	toggleNoticeKey := tb.InlineButton{
		Unique: "set_toggle_notice_btn",
		Text:   "Activar notificación",
		Data:   c.Data,
	}
	if sub.EnableNotification == 1 {
		toggleNoticeKey.Text = "Cerrar notificación"
	}

	toggleTelegraphKey := tb.InlineButton{
		Unique: "set_toggle_telegraph_btn",
		Text:   "Activar la transcodificación Telegraph",
		Data:   c.Data,
	}
	if sub.EnableTelegraph == 1 {
		toggleTelegraphKey.Text = "Desactivar la transcodificación Telegraph"
	}

	toggleEnabledKey := tb.InlineButton{
		Unique: "set_toggle_update_btn",
		Text:   "Pausar actualización",
		Data:   c.Data,
	}

	if source.ErrorCount >= config.ErrorThreshold {
		toggleEnabledKey.Text = "Reiniciar actualización"
	}

	feedSettingKeys := [][]tb.InlineButton{
		[]tb.InlineButton{
			toggleEnabledKey,
			toggleNoticeKey,
		},
		[]tb.InlineButton{
			toggleTelegraphKey,
			setSubTagKey,
		},
	}
	return feedSettingKeys
}

func setToggleNoticeBtnCtr(c *tb.Callback) {
	toggleCtrlButtons(c, "toggleNotice")
}

func setToggleTelegraphBtnCtr(c *tb.Callback) {
	toggleCtrlButtons(c, "toggleTelegraph")
}

func setToggleUpdateBtnCtr(c *tb.Callback) {
	toggleCtrlButtons(c, "toggleUpdate")
}

func unsubCmdCtr(m *tb.Message) {

	url, mention := GetURLAndMentionFromMessage(m)

	if mention == "" {
		if url != "" {
			//Unsub by url
			source, _ := model.GetSourceByUrl(url)
			if source == nil {
				_, _ = B.Send(m.Chat, "No suscrito a este canal RSS")
			} else {
				err := model.UnsubByUserIDAndSource(m.Chat.ID, source)
				if err == nil {
					_, _ = B.Send(
						m.Chat,
						fmt.Sprintf("[%s](%s) Dado de baja con éxito！", source.Title, source.Link),
						&tb.SendOptions{
							DisableWebPagePreview: true,
							ParseMode:             tb.ModeMarkdown,
						},
					)
					zap.S().Infof("%d unsubscribe [%d]%s %s", m.Chat.ID, source.ID, source.Title, source.Link)
				} else {
					_, err = B.Send(m.Chat, err.Error())
				}
			}
		} else {
			//Unsub by button

			subs, err := model.GetSubsByUserID(m.Chat.ID)

			if err != nil {
				errorCtr(m, "Error de bot, póngase en contacto con el administrador. Código de error 01")
				return
			}

			if len(subs) > 0 {
				unsubFeedItemBtns := [][]tb.InlineButton{}

				for _, sub := range subs {

					source, err := model.GetSourceById(sub.SourceID)
					if err != nil {
						errorCtr(m, "Error de bot, póngase en contacto con el administrador. Código de error 02")
						return
					}

					unsubFeedItemBtns = append(unsubFeedItemBtns, []tb.InlineButton{
						tb.InlineButton{
							Unique: "unsub_feed_item_btn",
							Text:   fmt.Sprintf("[%d] %s", sub.SourceID, source.Title),
							Data:   fmt.Sprintf("%d:%d:%d", sub.UserID, sub.ID, source.ID),
						},
					})
				}

				_, _ = B.Send(m.Chat, "Seleccione la fuente de la que desea cancelar la suscripción", &tb.ReplyMarkup{
					InlineKeyboard: unsubFeedItemBtns,
				})
			} else {
				_, _ = B.Send(m.Chat, "Actualmente no hay feeds")
			}
		}
	} else {
		if url != "" {
			channelChat, err := B.ChatByID(mention)
			if err != nil {
				_, _ = B.Send(m.Chat, "error")
				return
			}
			adminList, err := B.AdminsOf(channelChat)
			if err != nil {
				_, _ = B.Send(m.Chat, "error")
				return
			}

			senderIsAdmin := false
			for _, admin := range adminList {
				if m.Sender.ID == admin.User.ID {
					senderIsAdmin = true
				}
			}

			if !senderIsAdmin {
				_, _ = B.Send(m.Chat, fmt.Sprintf("Los administradores que no son de canal no pueden realizar esta operación"))
				return
			}

			source, _ := model.GetSourceByUrl(url)
			sub, err := model.GetSubByUserIDAndURL(channelChat.ID, url)

			if err != nil {
				if err.Error() == "record not found" {
					_, _ = B.Send(
						m.Chat,
						fmt.Sprintf("Canal [%s](https://t.me/%s) No suscrito a este canal RSS", channelChat.Title, channelChat.Username),
						&tb.SendOptions{
							DisableWebPagePreview: true,
							ParseMode:             tb.ModeMarkdown,
						},
					)

				} else {
					_, _ = B.Send(m.Chat, "No se pudo cancelar la suscripción")
				}
				return

			}

			err = sub.Unsub()
			if err == nil {
				_, _ = B.Send(
					m.Chat,
					fmt.Sprintf("Canal [%s](https://t.me/%s) Darse de baja [%s](%s) éxito", channelChat.Title, channelChat.Username, source.Title, source.Link),
					&tb.SendOptions{
						DisableWebPagePreview: true,
						ParseMode:             tb.ModeMarkdown,
					},
				)
				zap.S().Infof("%d for [%d]%s unsubscribe %s", m.Chat.ID, source.ID, source.Title, source.Link)
			} else {
				_, err = B.Send(m.Chat, err.Error())
			}
			return

		}
		_, _ = B.Send(m.Chat, "Para cancelar la suscripción del canal, utilice' /unsub @ChannelID URL ' mando")
	}

}

func unsubFeedItemBtnCtr(c *tb.Callback) {

	if (c.Message.Chat.Type == tb.ChatGroup || c.Message.Chat.Type == tb.ChatSuperGroup) &&
		!userIsAdminOfGroup(c.Sender.ID, c.Message.Chat) {
		// check admin
		return
	}

	data := strings.Split(c.Data, ":")
	if len(data) == 3 {
		userID, _ := strconv.Atoi(data[0])
		subID, _ := strconv.Atoi(data[1])
		sourceID, _ := strconv.Atoi(data[2])
		source, _ := model.GetSourceById(uint(sourceID))

		rtnMsg := fmt.Sprintf("[%d] <a href=\"%s\">%s</a> Darse de baja con éxito", sourceID, source.Link, source.Title)

		err := model.UnsubByUserIDAndSubID(int64(userID), uint(subID))

		if err == nil {
			_, _ = B.Edit(
				c.Message,
				rtnMsg,
				&tb.SendOptions{
					ParseMode: tb.ModeHTML,
				},
			)
			return
		}
	}
	_, _ = B.Edit(c.Message, "Error de cancelación de suscripción！")
}

func unsubAllCmdCtr(m *tb.Message) {
	mention := GetMentionFromMessage(m)
	confirmKeys := [][]tb.InlineButton{}
	confirmKeys = append(confirmKeys, []tb.InlineButton{
		tb.InlineButton{
			Unique: "unsub_all_confirm_btn",
			Text:   "confirmar",
		},
		tb.InlineButton{
			Unique: "unsub_all_cancel_btn",
			Text:   "cancelar",
		},
	})

	var msg string

	if mention == "" {
		msg = "Desea cancelar todas las suscripciones del usuario actual？"
	} else {
		msg = fmt.Sprintf("%s Desea darse de baja Channel Todas las suscripciones？", mention)
	}

	_, _ = B.Send(
		m.Chat,
		msg,
		&tb.SendOptions{
			ParseMode: tb.ModeHTML,
		}, &tb.ReplyMarkup{
			InlineKeyboard: confirmKeys,
		},
	)
}

func unsubAllCancelBtnCtr(c *tb.Callback) {
	_, _ = B.Edit(c.Message, "Operación cancelada")
}

func unsubAllConfirmBtnCtr(c *tb.Callback) {
	mention := GetMentionFromMessage(c.Message)
	var msg string
	if mention == "" {
		success, fail, err := model.UnsubAllByUserID(int64(c.Sender.ID))
		if err != nil {
			msg = "No se pudo cancelar la suscripción"
		} else {
			msg = fmt.Sprintf("Darse de baja con éxito：%d\nNo se pudo cancelar la suscripción：%d", success, fail)
		}

	} else {
		channelChat, err := B.ChatByID(mention)

		if err != nil {
			_, _ = B.Edit(c.Message, "error")
			return
		}

		if UserIsAdminChannel(c.Sender.ID, channelChat) {
			success, fail, err := model.UnsubAllByUserID(channelChat.ID)
			if err != nil {
				msg = "No se pudo cancelar la suscripción"

			} else {
				msg = fmt.Sprintf("Darse de baja con éxito：%d\nNo se pudo cancelar la suscripción：%d", success, fail)
			}

		} else {
			msg = "Los administradores que no son de canal no pueden realizar esta operación"
		}
	}

	_, _ = B.Edit(c.Message, msg)
}

func pingCmdCtr(m *tb.Message) {
	_, _ = B.Send(m.Chat, "pong")
	zap.S().Debugw(
		"pong",
		"telegram msg", m,
	)
}

func helpCmdCtr(m *tb.Message) {
	message := `
Comandos：
/sub Alimentar
/unsub  darse de baja
/list Ver feeds actuales
/set Configurar suscripción
/check Verificar suscripción actual
/setfeedtag Establecer etiqueta de suscripción
/setinterval Establecer la frecuencia de actualización de la suscripción
/activeall Activar todas las suscripciones
/pauseall Suspender todas las suscripciones
/help ayuda
/import Importar archivos OPML
/export Exportar archivos OPML
/unsuball Cancelar todas las suscripciones
Para obtener información detallada sobre el uso, consulte：https://github.com/indes/flowerss-bot
`

	_, _ = B.Send(m.Chat, message)
}

func versionCmdCtr(m *tb.Message) {
	_, _ = B.Send(m.Chat, config.AppVersionInfo())
}

func importCmdCtr(m *tb.Message) {
	message := `Envíe el archivo OPML directamente，
Si necesita importar OPML para el canal, incluya la identificación del canal al enviar el archivo, como @telegram
`
	_, _ = B.Send(m.Chat, message)
}

func setFeedTagCmdCtr(m *tb.Message) {
	mention := GetMentionFromMessage(m)
	args := strings.Split(m.Payload, " ")

	if len(args) < 1 {
		B.Send(m.Chat, "/setfeedtag [sub id] [tag1] [tag2] Establecer etiquetas de suscripción (configurar hasta tres etiquetas, separadas por espacios）")
		return
	}

	var subID int
	var err error
	if mention == "" {
		// 截短参数
		if len(args) > 4 {
			args = args[:4]
		}
		subID, err = strconv.Atoi(args[0])
		if err != nil {
			B.Send(m.Chat, "Ingrese el ID de suscripción correcto!")
			return
		}
	} else {
		if len(args) > 5 {
			args = args[:5]
		}
		subID, err = strconv.Atoi(args[1])
		if err != nil {
			B.Send(m.Chat, "Ingrese el ID de suscripción correcto!")
			return
		}
	}

	sub, err := model.GetSubscribeByID(subID)
	if err != nil || sub == nil {
		B.Send(m.Chat, "Ingrese el ID de suscripción correcto")
		return
	}

	if !checkPermit(int64(m.Sender.ID), sub.UserID) {
		B.Send(m.Chat, "¡Permiso denegado!")
		return
	}

	if mention == "" {
		err = sub.SetTag(args[1:])
	} else {
		err = sub.SetTag(args[2:])
	}

	if err != nil {
		B.Send(m.Chat, "No se pudo establecer la etiqueta de suscripción!")
		return
	}
	B.Send(m.Chat, "La etiqueta de suscripción se estableció correctamente!")
}

func setIntervalCmdCtr(m *tb.Message) {

	args := strings.Split(m.Payload, " ")

	if len(args) < 1 {
		_, _ = B.Send(m.Chat, "/setinterval [interval] [sub id] Establecer la frecuencia de actualización de la suscripción (se pueden establecer múltiples sub id, separados por espacios）")
		return
	}

	interval, err := strconv.Atoi(args[0])
	if interval <= 0 || err != nil {
		_, _ = B.Send(m.Chat, "Ingrese la frecuencia de rastreo correcta")
		return
	}

	for _, id := range args[1:] {

		subID, err := strconv.Atoi(id)
		if err != nil {
			_, _ = B.Send(m.Chat, "Ingrese el ID de suscripción correcto!")
			return
		}

		sub, err := model.GetSubscribeByID(subID)

		if err != nil || sub == nil {
			_, _ = B.Send(m.Chat, "Ingrese el ID de suscripción correcto!")
			return
		}

		if !checkPermit(int64(m.Sender.ID), sub.UserID) {
			_, _ = B.Send(m.Chat, "Permiso denegado!")
			return
		}

		_ = sub.SetInterval(interval)

	}
	_, _ = B.Send(m.Chat, "La frecuencia de rastreo se estableció correctamente!")

	return
}

func activeAllCmdCtr(m *tb.Message) {
	mention := GetMentionFromMessage(m)
	if mention != "" {
		channelChat, err := B.ChatByID(mention)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}
		adminList, err := B.AdminsOf(channelChat)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}

		senderIsAdmin := false
		for _, admin := range adminList {
			if m.Sender.ID == admin.User.ID {
				senderIsAdmin = true
			}
		}

		if !senderIsAdmin {
			_, _ = B.Send(m.Chat, fmt.Sprintf("Los administradores que no son de canal no pueden realizar esta operación"))
			return
		}

		_ = model.ActiveSourcesByUserID(channelChat.ID)
		message := fmt.Sprintf("Canal [%s](https://t.me/%s) Todas las suscripciones están activadas", channelChat.Title, channelChat.Username)

		_, _ = B.Send(m.Chat, message, &tb.SendOptions{
			DisableWebPagePreview: true,
			ParseMode:             tb.ModeMarkdown,
		})

	} else {
		_ = model.ActiveSourcesByUserID(m.Chat.ID)
		message := "Todas las suscripciones están activadas"

		_, _ = B.Send(m.Chat, message, &tb.SendOptions{
			DisableWebPagePreview: true,
			ParseMode:             tb.ModeMarkdown,
		})
	}

}

func pauseAllCmdCtr(m *tb.Message) {
	mention := GetMentionFromMessage(m)
	if mention != "" {
		channelChat, err := B.ChatByID(mention)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}
		adminList, err := B.AdminsOf(channelChat)
		if err != nil {
			_, _ = B.Send(m.Chat, "error")
			return
		}

		senderIsAdmin := false
		for _, admin := range adminList {
			if m.Sender.ID == admin.User.ID {
				senderIsAdmin = true
			}
		}

		if !senderIsAdmin {
			_, _ = B.Send(m.Chat, fmt.Sprintf("Los administradores que no son de canal no pueden realizar esta operación"))
			return
		}

		_ = model.PauseSourcesByUserID(channelChat.ID)
		message := fmt.Sprintf("Canal [%s](https://t.me/%s) Todas las suscripciones están suspendidas", channelChat.Title, channelChat.Username)

		_, _ = B.Send(m.Chat, message, &tb.SendOptions{
			DisableWebPagePreview: true,
			ParseMode:             tb.ModeMarkdown,
		})

	} else {
		_ = model.PauseSourcesByUserID(m.Chat.ID)
		message := "Todas las suscripciones están suspendidas"

		_, _ = B.Send(m.Chat, message, &tb.SendOptions{
			DisableWebPagePreview: true,
			ParseMode:             tb.ModeMarkdown,
		})
	}

}

func textCtr(m *tb.Message) {
	switch UserState[m.Chat.ID] {
	case fsm.UnSub:
		{
			str := strings.Split(m.Text, " ")

			if len(str) < 2 && (strings.HasPrefix(str[0], "[") && strings.HasSuffix(str[0], "]")) {
				_, _ = B.Send(m.Chat, "Elija la instrucción correcta！")
			} else {

				var sourceID uint
				if _, err := fmt.Sscanf(str[0], "[%d]", &sourceID); err != nil {
					_, _ = B.Send(m.Chat, "Elija la instrucción correcta！")
					return
				}

				source, err := model.GetSourceById(sourceID)

				if err != nil {
					_, _ = B.Send(m.Chat, "Elija la instrucción correcta！")
					return
				}

				err = model.UnsubByUserIDAndSource(m.Chat.ID, source)

				if err != nil {
					_, _ = B.Send(m.Chat, "Elija la instrucción correcta！")
					return
				}

				_, _ = B.Send(
					m.Chat,
					fmt.Sprintf("[%s](%s) Darse de baja con éxito", source.Title, source.Link),
					&tb.SendOptions{
						ParseMode: tb.ModeMarkdown,
					}, &tb.ReplyMarkup{
						ReplyKeyboardRemove: true,
					},
				)
				UserState[m.Chat.ID] = fsm.None
				return
			}
		}

	case fsm.Sub:
		{
			url := strings.Split(m.Text, " ")
			if !CheckURL(url[0]) {
				_, _ = B.Send(m.Chat, "Responde a la URL correcta.", &tb.ReplyMarkup{ForceReply: true})
				return
			}

			registFeed(m.Chat, url[0])
			UserState[m.Chat.ID] = fsm.None
		}
	case fsm.SetSubTag:
		{
			return
		}
	case fsm.Set:
		{
			str := strings.Split(m.Text, " ")
			url := str[len(str)-1]
			if len(str) != 2 && !CheckURL(url) {
				_, _ = B.Send(m.Chat, "Elija la instrucción correcta！")
			} else {
				source, err := model.GetSourceByUrl(url)

				if err != nil {
					_, _ = B.Send(m.Chat, "Elija la instrucción correcta！")
					return
				}
				sub, err := model.GetSubscribeByUserIDAndSourceID(m.Chat.ID, source.ID)
				if err != nil {
					_, _ = B.Send(m.Chat, "Elija la instrucción correcta！")
					return
				}
				t := template.New("setting template")
				_, _ = t.Parse(feedSettingTmpl)

				toggleNoticeKey := tb.InlineButton{
					Unique: "set_toggle_notice_btn",
					Text:   "Activar notificación",
				}
				if sub.EnableNotification == 1 {
					toggleNoticeKey.Text = "Cerrar notificación"
				}

				toggleTelegraphKey := tb.InlineButton{
					Unique: "set_toggle_telegraph_btn",
					Text:   "Activar la transcodificación Telegraph",
				}
				if sub.EnableTelegraph == 1 {
					toggleTelegraphKey.Text = "Desactivar la transcodificación Telegraph"
				}

				toggleEnabledKey := tb.InlineButton{
					Unique: "set_toggle_update_btn",
					Text:   "Pausar actualización",
				}

				if source.ErrorCount >= config.ErrorThreshold {
					toggleEnabledKey.Text = "Reiniciar actualización"
				}

				feedSettingKeys := [][]tb.InlineButton{
					[]tb.InlineButton{
						toggleEnabledKey,
						toggleNoticeKey,
						toggleTelegraphKey,
					},
				}

				text := new(bytes.Buffer)

				_ = t.Execute(text, map[string]interface{}{"source": source, "sub": sub, "Count": config.ErrorThreshold})

				// send null message to remove old keyboard
				delKeyMessage, err := B.Send(m.Chat, "processing", &tb.ReplyMarkup{ReplyKeyboardRemove: true})
				err = B.Delete(delKeyMessage)

				_, _ = B.Send(
					m.Chat,
					text.String(),
					&tb.SendOptions{
						ParseMode: tb.ModeHTML,
					}, &tb.ReplyMarkup{
						InlineKeyboard: feedSettingKeys,
					},
				)
				UserState[m.Chat.ID] = fsm.None
			}
		}
	}
}

// docCtr Document handler
func docCtr(m *tb.Message) {
	if m.FromGroup() {
		if !userIsAdminOfGroup(m.Sender.ID, m.Chat) {
			return
		}
	}

	if m.FromChannel() {
		if !UserIsAdminChannel(m.ID, m.Chat) {
			return
		}
	}

	url, _ := B.FileURLByID(m.Document.FileID)
	if !strings.HasSuffix(url, ".opml") {
		B.Send(m.Chat, "Si necesita importar suscripciones, envíe el archivo OPML correcto.")
		return
	}

	opml, err := GetOPMLByURL(url)
	if err != nil {
		if err.Error() == "fetch opml file error" {
			_, _ = B.Send(m.Chat,
				"No se pudo descargar el archivo OPML. Verifique si el servidor bot puede conectarse al servidor de Telegram o intente importarlo más tarde. Código de error 02")

		} else {
			_, _ = B.Send(
				m.Chat,
				fmt.Sprintf(
					"Si necesita importar suscripciones, envíe el archivo OPML correcto. Código de error 01, doc mimetype: %s",
					m.Document.MIME),
			)
		}
		return
	}

	userID := m.Chat.ID
	mention := GetMentionFromMessage(m)
	if mention != "" {
		// import for channel
		channelChat, err := B.ChatByID(mention)
		if err != nil {
			_, _ = B.Send(m.Chat, "Obtenga el error de información del canal, verifique si la identificación del canal es correcta")
			return
		}

		if !checkPermitOfChat(int64(m.Sender.ID), channelChat) {
			_, _ = B.Send(m.Chat, fmt.Sprintf("Los administradores que no son de canal no pueden realizar esta operación"))
			return
		}

		userID = channelChat.ID
	}

	message, _ := B.Send(m.Chat, "Procesando .. por favor espere...")
	outlines, _ := opml.GetFlattenOutlines()
	var failImportList []Outline
	var successImportList []Outline

	for _, outline := range outlines {
		source, err := model.FindOrNewSourceByUrl(outline.XMLURL)
		if err != nil {
			failImportList = append(failImportList, outline)
			continue
		}
		err = model.RegistFeed(userID, source.ID)
		if err != nil {
			failImportList = append(failImportList, outline)
			continue
		}
		zap.S().Infof("%d subscribe [%d]%s %s", m.Chat.ID, source.ID, source.Title, source.Link)
		successImportList = append(successImportList, outline)
	}

	importReport := fmt.Sprintf("<b>Importación exitosa: %d, error de importación：%d</b>", len(successImportList), len(failImportList))
	if len(successImportList) != 0 {
		successReport := "\n\n<b>Los siguientes feeds se importaron correctamente:</b>"
		for i, line := range successImportList {
			if line.Text != "" {
				successReport += fmt.Sprintf("\n[%d] <a href=\"%s\">%s</a>", i+1, line.XMLURL, line.Text)
			} else {
				successReport += fmt.Sprintf("\n[%d] %s", i+1, line.XMLURL)
			}
		}
		importReport += successReport
	}

	if len(failImportList) != 0 {
		failReport := "\n\n<b>No se pudo importar el siguiente feed:</b>"
		for i, line := range failImportList {
			if line.Text != "" {
				failReport += fmt.Sprintf("\n[%d] <a href=\"%s\">%s</a>", i+1, line.XMLURL, line.Text)
			} else {
				failReport += fmt.Sprintf("\n[%d] %s", i+1, line.XMLURL)
			}
		}
		importReport += failReport
	}

	_, _ = B.Edit(message, importReport, &tb.SendOptions{
		DisableWebPagePreview: true,
		ParseMode:             tb.ModeHTML,
	})
}

func errorCtr(m *tb.Message, errMsg string) {
	_, _ = B.Send(m.Chat, errMsg)
}
