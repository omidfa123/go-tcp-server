package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
)

const (
	CreateSchoolMethod      = "/school/create"
	CreateClassMethod       = "/class/create"
	CreatePersonMethod      = "/person/create"
	AddStudentToClassMethod = "/class/add/student"
	WhoAmIMethod            = "/who/am/i"
)

var db *sql.DB

func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "./data.db")
	if err != nil {
		return err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS schools (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS classes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			school_id INTEGER NOT NULL,
			teacher_id INTEGER,
			FOREIGN KEY (school_id) REFERENCES schools(id),
			FOREIGN KEY (teacher_id) REFERENCES persons(id)
		);
		CREATE TABLE IF NOT EXISTS persons (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			role TEXT DEFAULT '',
			school_id INTEGER DEFAULT NULL,
			FOREIGN KEY (school_id) REFERENCES schools(id)
		);
		CREATE TABLE IF NOT EXISTS class_students (
			class_id INTEGER,
			person_id INTEGER,
			PRIMARY KEY (class_id, person_id),
			FOREIGN KEY (class_id) REFERENCES classes(id),
			FOREIGN KEY (person_id) REFERENCES persons(id)
		);
	`)
	return err
}

func handleConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	for {
		message, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		var req Request
		if err := json.Unmarshal([]byte(message), &req); err != nil {
			sendResponse(conn, Response{Status: false, Message: "invalid json"})
			continue
		}

		var resp Response

		switch req.Method {
		case CreateSchoolMethod:
			resp = handleCreateSchool(req.Data)
		case CreateClassMethod:
			resp = handleCreateClass(req.Data)
		case CreatePersonMethod:
			resp = handleCreatePerson(req.Data)
		case AddStudentToClassMethod:
			resp = handleAddStudentToClass(req.Data)
		case WhoAmIMethod:
			resp = handleWhoAmI(req.Data)
		default:
			resp = Response{Status: false, Message: "unknown route"}
		}

		sendResponse(conn, resp)
	}
}

func sendResponse(conn net.Conn, resp Response) {
	data, _ := json.Marshal(resp)
	conn.Write(append(data, '\n'))
}

func main() {
	server, err := NewServer("8090")
	if err != nil {
		log.Fatal("Failed to create server:", err)
	}
	defer db.Close()

	go func() {
		server.Start()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	server.Stop()
}

type Server interface {
	Start() error
	Stop() error
}

type server struct {
	listener net.Listener
}

func NewServer(port string) (Server, error) {
	if db == nil {
		if err := initDB(); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return nil, err
	}
	return &server{listener: listener}, nil
}

func (s *server) Start() error {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return nil
		}
		go handleConnection(conn)
	}
}

func (s *server) Stop() error {
	if s.listener == nil {
		return nil
	}
	err := s.listener.Close()
	if err != nil {
		log.Println("Error closing connection", err)
	}
	return nil
}

func handleCreateSchool(data interface{}) Response {
	jsonData, _ := json.Marshal(data)
	var school School
	if err := json.Unmarshal(jsonData, &school); err != nil {
		return Response{Status: false, Message: "invalid school data"}
	}
	result, err := db.Exec("INSERT INTO schools (name) VALUES (?)", school.Name)
	if err != nil {
		return Response{Status: false, Message: err.Error()}
	}
	id, _ := result.LastInsertId()
	school.Id = uint(id)
	school.Classes = nil
	return Response{Status: true, Message: "school created", Data: school}
}

func handleCreatePerson(data interface{}) Response {
	jsonData, _ := json.Marshal(data)
	var person Person
	if err := json.Unmarshal(jsonData, &person); err != nil {
		return Response{Status: false, Message: "invalid person data"}
	}
	result, err := db.Exec("INSERT INTO persons (name, role) VALUES (?, '')", person.Name)
	if err != nil {
		return Response{Status: false, Message: err.Error()}
	}
	id, _ := result.LastInsertId()
	person.Id = uint(id)
	person.Classes = nil
	return Response{Status: true, Message: "person created", Data: person}
}

func handleCreateClass(data interface{}) Response {
	jsonData, _ := json.Marshal(data)
	var class Class
	if err := json.Unmarshal(jsonData, &class); err != nil {
		return Response{Status: false, Message: "invalid class data"}
	}

	var schoolExists int
	err := db.QueryRow("SELECT COUNT(*) FROM schools WHERE id = ?", class.SchoolId).Scan(&schoolExists)
	if err != nil || schoolExists == 0 {
		return Response{Status: false, Message: "school not found"}
	}

	var teacherRole string
	err = db.QueryRow("SELECT role FROM persons WHERE id = ?", class.Teacher.Id).Scan(&teacherRole)
	if err != nil {
		return Response{Status: false, Message: "teacher not found"}
	}

	if teacherRole == "student" {
		return Response{Status: false, Message: "person is already a student, cannot be a teacher"}
	}

	_, err = db.Exec("UPDATE persons SET role = 'teacher' WHERE id = ?", class.Teacher.Id)
	if err != nil {
		return Response{Status: false, Message: err.Error()}
	}

	result, err := db.Exec("INSERT INTO classes (name, school_id, teacher_id) VALUES (?, ?, ?)",
		class.Name, class.SchoolId, class.Teacher.Id)
	if err != nil {
		return Response{Status: false, Message: err.Error()}
	}
	id, _ := result.LastInsertId()
	class.Id = uint(id)
	class.Students = nil
	return Response{Status: true, Message: "class created", Data: class}
}

func handleAddStudentToClass(data interface{}) Response {
	jsonData, _ := json.Marshal(data)
	var req AddStudentToClassReq
	if err := json.Unmarshal(jsonData, &req); err != nil {
		return Response{Status: false, Message: "invalid request data"}
	}

	var person Person
	var role string
	var studentSchoolId sql.NullInt64
	err := db.QueryRow("SELECT id, name, role, school_id FROM persons WHERE id = ?", req.StudentId).
		Scan(&person.Id, &person.Name, &role, &studentSchoolId)
	if err != nil {
		return Response{Status: false, Message: "student not found"}
	}

	if role == "teacher" {
		return Response{Status: false, Message: "person is a teacher, cannot be a student"}
	}

	var classSchoolId uint
	err = db.QueryRow("SELECT school_id FROM classes WHERE id = ?", req.ClassId).Scan(&classSchoolId)
	if err != nil {
		return Response{Status: false, Message: "class not found"}
	}

	if studentSchoolId.Valid && uint(studentSchoolId.Int64) != classSchoolId {
		return Response{Status: false, Message: "student can only enroll in classes from one school"}
	}

	_, err = db.Exec("UPDATE persons SET role = 'student', school_id = ? WHERE id = ?", classSchoolId, req.StudentId)
	if err != nil {
		return Response{Status: false, Message: err.Error()}
	}

	_, err = db.Exec("INSERT INTO class_students (class_id, person_id) VALUES (?, ?)", req.ClassId, req.StudentId)
	if err != nil {
		return Response{Status: false, Message: err.Error()}
	}

	rows, err := db.Query("SELECT class_id FROM class_students WHERE person_id = ?", req.StudentId)
	if err != nil {
		return Response{Status: false, Message: err.Error()}
	}
	defer rows.Close()

	var classes []uint
	for rows.Next() {
		var classId uint
		rows.Scan(&classId)
		classes = append(classes, classId)
	}
	person.Classes = classes

	return Response{Status: true, Message: "student added to class", Data: person}
}

func handleWhoAmI(data interface{}) Response {
	jsonData, _ := json.Marshal(data)
	var reqPerson Person
	if err := json.Unmarshal(jsonData, &reqPerson); err != nil {
		return Response{Status: false, Message: "invalid request data"}
	}

	var person Person
	var role string
	err := db.QueryRow("SELECT id, name, role FROM persons WHERE id = ?", reqPerson.Id).
		Scan(&person.Id, &person.Name, &role)
	if err != nil {
		return Response{Status: false, Message: "person not found"}
	}

	var classes []uint
	if role == "teacher" {
		rows, err := db.Query("SELECT id FROM classes WHERE teacher_id = ?", person.Id)
		if err != nil {
			return Response{Status: false, Message: err.Error()}
		}
		defer rows.Close()
		for rows.Next() {
			var classId uint
			rows.Scan(&classId)
			classes = append(classes, classId)
		}
	} else if role == "student" {
		rows, err := db.Query("SELECT class_id FROM class_students WHERE person_id = ?", person.Id)
		if err != nil {
			return Response{Status: false, Message: err.Error()}
		}
		defer rows.Close()
		for rows.Next() {
			var classId uint
			rows.Scan(&classId)
			classes = append(classes, classId)
		}
	}
	person.Classes = classes

	return Response{Status: true, Message: "success", Data: person}
}
