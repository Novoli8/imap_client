package main

import (
    "crypto/tls"
    "fmt"
    "io"
    "log"
    "net/mail"
    "regexp"
    "strconv"
    "strings"

    "fyne.io/fyne/v2"
    "fyne.io/fyne/v2/app"
    "fyne.io/fyne/v2/container"
    "fyne.io/fyne/v2/dialog"
    "fyne.io/fyne/v2/widget"
    "github.com/emersion/go-imap"
    "github.com/emersion/go-imap/client"
    "github.com/emersion/go-message"
)

// Email представляет структуру письма
type Email struct {
    UID     uint32
    From    string
    Subject string
}

// AppData хранит данные приложения, включая IMAP-клиента и список писем
type AppData struct {
    Client  *client.Client
    Emails  []Email
    EmailCh chan []Email
}

func main() {
    a := app.New()
    w := a.NewWindow("IMAP Клиент")
    w.Resize(fyne.NewSize(600, 400))

    appData := &AppData{
        EmailCh: make(chan []Email),
    }

    // Создаем интерфейс авторизации
    loginForm := createLoginForm(a, w, appData)

    w.SetContent(loginForm)
    w.ShowAndRun()
}

// createLoginForm создает форму авторизации
func createLoginForm(a fyne.App, w fyne.Window, appData *AppData) fyne.CanvasObject {
    emailEntry := widget.NewEntry()
    emailEntry.SetPlaceHolder("Email")
    passwordEntry := widget.NewPasswordEntry()
    passwordEntry.SetPlaceHolder("Пароль")
    loginButton := widget.NewButton("Войти", func() {
        email := strings.TrimSpace(emailEntry.Text)
        password := strings.TrimSpace(passwordEntry.Text)

        if email == "" || password == "" {
            dialog.ShowError(fmt.Errorf("Пожалуйста, заполните все поля"), w)
            return
        }

        server, port := getIMAPServer(email)
        if server == "" || port == 0 {
            dialog.ShowError(fmt.Errorf("Не удалось определить IMAP сервер для данного домена"), w)
            return
        }

        // Подключение к серверу
        c, err := client.DialTLS(fmt.Sprintf("%s:%d", server, port), &tls.Config{InsecureSkipVerify: true})
        if err != nil {
            dialog.ShowError(fmt.Errorf("Не удалось подключиться к серверу: %v", err), w)
            return
        }

        // Аутентификация
        if err := c.Login(email, password); err != nil {
            c.Logout()
            dialog.ShowError(fmt.Errorf("Ошибка аутентификации: %v", err), w)
            return
        }

        appData.Client = c

        // Выбор INBOX
        mbox, err := c.Select("INBOX", false)
        if err != nil {
            c.Logout()
            dialog.ShowError(fmt.Errorf("Не удалось выбрать INBOX: %v", err), w)
            return
        }

        if mbox.Messages == 0 {
            c.Logout()
            dialog.ShowInformation("INBOX пуст", "Нет сообщений в INBOX", w)
            return
        }

        // Получение последних 10 писем
        from := uint32(1)
        if mbox.Messages > 10 {
            from = mbox.Messages - 9
        }
        to := mbox.Messages

        seqset := new(imap.SeqSet)
        seqset.AddRange(from, to)

        messages := make(chan *imap.Message, 10)
        done := make(chan error, 1)
        go func() {
            done <- c.Fetch(seqset, []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}, messages)
        }()

        var emails []Email
        for msg := range messages {
            if len(msg.Envelope.From) > 0 {
                emails = append(emails, Email{
                    UID:     msg.Uid,
                    From:    msg.Envelope.From[0].Address(),
                    Subject: msg.Envelope.Subject,
                })
            }
        }

        if err := <-done; err != nil {
            c.Logout()
            dialog.ShowError(fmt.Errorf("Ошибка при получении писем: %v", err), w)
            return
        }

        appData.Emails = emails

        // Закрываем соединение (будем использовать его в других функциях через appData)
        // Не закрываем здесь, чтобы использовать в дальнейшем
        // c.Logout()

        // Переходим к списку писем
        emailList := createEmailList(a, w, appData)
        w.SetContent(emailList)
    })

    loginForm := container.NewVBox(
        widget.NewLabel("Авторизация"),
        emailEntry,
        passwordEntry,
        loginButton,
    )

    return container.NewCenter(loginForm)
}

// createEmailList создает интерфейс для отображения списка писем
func createEmailList(a fyne.App, w fyne.Window, appData *AppData) fyne.CanvasObject {
    list := widget.NewList(
        func() int {
            return len(appData.Emails)
        },
        func() fyne.CanvasObject {
            return widget.NewLabel("Template")
        },
        func(i widget.ListItemID, o fyne.CanvasObject) {
            email := appData.Emails[i]
            o.(*widget.Label).SetText(fmt.Sprintf("От: %s | Тема: %s", email.From, email.Subject))
        },
    )

    list.OnSelected = func(id widget.ListItemID) {
        selectedEmail := appData.Emails[id]
        showEmailContent(a, w, appData, selectedEmail)
        list.Unselect(id)
    }

    refreshButton := widget.NewButton("Обновить список", func() {
        go fetchEmails(a, w, appData)
    })

    logoutButton := widget.NewButton("Выйти", func() {
        if appData.Client != nil {
            appData.Client.Logout()
        }
        // Переходим к форме авторизации
        loginForm := createLoginForm(a, w, appData)
        w.SetContent(loginForm)
    })

    buttons := container.NewHBox(refreshButton, logoutButton)
    content := container.NewBorder(buttons, nil, nil, nil, list)
    return content
}

// fetchEmails обновляет список писем
func fetchEmails(a fyne.App, w fyne.Window, appData *AppData) {
    if appData.Client == nil {
        dialog.ShowError(fmt.Errorf("Клиент не подключен"), w)
        return
    }

    mbox, err := appData.Client.Select("INBOX", false)
    if err != nil {
        dialog.ShowError(fmt.Errorf("Не удалось выбрать INBOX: %v", err), w)
        return
    }

    if mbox.Messages == 0 {
        dialog.ShowInformation("INBOX пуст", "Нет сообщений в INBOX", w)
        return
    }

    // Получение последних 10 писем
    from := uint32(1)
    if mbox.Messages > 10 {
        from = mbox.Messages - 9
    }
    to := mbox.Messages

    seqset := new(imap.SeqSet)
    seqset.AddRange(from, to)

    messages := make(chan *imap.Message, 10)
    done := make(chan error, 1)
    go func() {
        done <- appData.Client.Fetch(seqset, []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}, messages)
    }()

    var emails []Email
    for msg := range messages {
        if len(msg.Envelope.From) > 0 {
            emails = append(emails, Email{
                UID:     msg.Uid,
                From:    msg.Envelope.From[0].Address(),
                Subject: msg.Envelope.Subject,
            })
        }
    }

    if err := <-done; err != nil {
        dialog.ShowError(fmt.Errorf("Ошибка при получении писем: %v", err), w)
        return
    }

    appData.Emails = emails

    // Обновляем список писем
    emailList := createEmailList(a, w, appData)
    w.SetContent(emailList)
}

// showEmailContent отображает содержимое выбранного письма
func showEmailContent(a fyne.App, w fyne.Window, appData *AppData, email Email) {
    content := widget.NewMultiLineEntry()
    content.SetPlaceHolder("Загрузка содержимого...")
    content.SetReadOnly(true)

    deleteButton := widget.NewButton("Удалить письмо", func() {
        confirm := dialog.NewConfirm("Удаление", "Вы уверены, что хотите удалить это письмо?", func(confirmed bool) {
            if confirmed {
                err := deleteEmail(appData, email, w)
                if err != nil {
                    dialog.ShowError(err, w)
                    return
                }
                dialog.ShowInformation("Успешно", "Письмо удалено", w)
                // Обновляем список писем
                fetchEmails(a, w, appData)
                // Закрываем окно просмотра
                w.Close()
            }
        }, w)
        confirm.Show()
    })

    backButton := widget.NewButton("Назад", func() {
        emailList := createEmailList(a, w, appData)
        w.SetContent(emailList)
    })

    buttons := container.NewHBox(deleteButton, backButton)
    layout := container.NewBorder(buttons, nil, nil, nil, content)
    w.SetContent(layout)

    // Загрузка содержимого письма в отдельной горутине
    go func() {
        body, err := viewMessage(appData.Client, email.UID)
        if err != nil {
            fyne.CurrentApp().SendNotification(&fyne.Notification{
                Title:   "Ошибка",
                Content: fmt.Sprintf("Не удалось загрузить письмо: %v", err),
            })
            return
        }
        content.SetText(body)
    }()
}

// viewMessage загружает содержимое письма по UID
func viewMessage(c *client.Client, uid uint32) (string, error) {
    seqset := new(imap.SeqSet)
    seqset.AddNum(uid)

    section := &imap.BodySectionName{}
    messages := make(chan *imap.Message, 1)
    done := make(chan error, 1)
    go func() {
        done <- c.UidFetch(seqset, []imap.FetchItem{section.FetchItem(), imap.FetchUid}, messages)
    }()

    msg := <-messages
    if msg == nil {
        return "", fmt.Errorf("сообщение не найдено")
    }

    r := msg.GetBody(section)
    if r == nil {
        return "", fmt.Errorf("не удалось получить тело сообщения")
    }

    mr, err := message.Read(r)
    if err != nil {
        return "", err
    }

    // Заголовки письма
    header := mr.Header
    subject, _ := header.Subject()
    fromList, err := header.AddressList("From")
    if err != nil {
        return "", err
    }
    from := ""
    if len(fromList) > 0 {
        from = fromList[0].String()
    }

    // Тело письма
    body, err := io.ReadAll(mr.Body)
    if err != nil {
        return "", err
    }

    content := fmt.Sprintf("От: %s\nТема: %s\n\n%s", from, subject, string(body))

    if err := <-done; err != nil {
        return "", err
    }

    return content, nil
}

// deleteEmail удаляет письмо по UID
func deleteEmail(appData *AppData, email Email, w fyne.Window) error {
    if appData.Client == nil {
        return fmt.Errorf("клиент не подключен")
    }

    seqset := new(imap.SeqSet)
    seqset.AddNum(email.UID)

    // Устанавливаем флаг удаления
    item := imap.FormatFlagsOp(imap.AddFlags, true)
    flags := []interface{}{imap.DeletedFlag}
    if err := appData.Client.UidStore(seqset, item, flags, nil); err != nil {
        return fmt.Errorf("не удалось установить флаг удаления: %v", err)
    }

    // Выполняем expunge для окончательного удаления письма
    if err := appData.Client.Expunge(nil); err != nil {
        return fmt.Errorf("не удалось выполнить expunge: %v", err)
    }

    return nil
}

// getIMAPServer определяет IMAP-сервер и порт по домену email
func getIMAPServer(email string) (string, int) {
    // Извлекаем домен из email
    re := regexp.MustCompile(`@(.+)$`)
    matches := re.FindStringSubmatch(email)
    if len(matches) != 2 {
        return "", 0
    }
    domain := matches[1]

    // Популярные почтовые сервисы
    servers := map[string]string{
        "gmail.com":    "imap.gmail.com",
        "yahoo.com":    "imap.mail.yahoo.com",
        "outlook.com":  "imap-mail.outlook.com",
        "hotmail.com":  "imap-mail.outlook.com",
        "mail.ru":      "imap.mail.ru",
        "yandex.ru":    "imap.yandex.ru",
    }

    server, ok := servers[domain]
    if !ok {
        // Пробуем стандартные варианты
        server = "imap." + domain
    }

    // Стандартный порт для IMAP over SSL/TLS
    port := 993

    return server, port
}
