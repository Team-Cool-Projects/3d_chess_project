package model

import (
	"encoding/json"
	"fmt"
	"github.com/team142/chessfor4/io/ws"
	"log"
)

//CreateServer starts a new server
func CreateServer(address string, handler func(*Server, *ws.Client, []byte), canStartBeforeFull bool) *Server {
	s := &Server{
		Address:            address,
		handler:            handler,
		Lobby:              make(map[*ws.Client]*Profile),
		Games:              make(map[string]*Game),
		todo:               make(chan *item, 256),
		canStartBeforeFull: canStartBeforeFull,
	}
	s.run()
	return s
}

//Server holds server state
type Server struct {
	Address            string
	Lobby              map[*ws.Client]*Profile
	Games              map[string]*Game
	handler            func(*Server, *ws.Client, []byte)
	todo               chan *item
	canStartBeforeFull bool
}

type item struct {
	client *ws.Client
	msg    []byte
}

func (s *Server) run() {
	go func() {
		for i := range s.todo {
			s.handler(s, i.client, i.msg)
		}
	}()
}

//GameByClientOwner finds a game owned by client
func (s *Server) GameByClientOwner(client *ws.Client) (found bool, game *Game) {
	for _, game := range s.Games {
		if game.Owner.Profile.Client == client {
			return true, game
		}
	}
	return
}

//GameByClientPlaying find any player in a game
func (s *Server) GameByClientPlaying(client *ws.Client) (found bool, game *Game) {
	for _, game := range s.Games {
		for _, player := range game.Players {
			if player.Profile.Client == client {
				return true, game
			}
		}
	}
	return
}

//HandleMessage This message is called by other parts of the system - the interface to the server
func (s *Server) HandleMessage(client *ws.Client, msg []byte) {
	i := &item{
		client: client,
		msg:    msg,
	}
	s.todo <- i

}

//GetOrCreateProfile creates profiles from a websocket client
func (s *Server) GetOrCreateProfile(client *ws.Client) *Profile {
	p := s.Lobby[client]
	if p == nil {
		p = CreateProfile(client)
		s.Lobby[client] = p
	}
	return p
}

//CreateGame for easy access
func (s *Server) CreateGame(client *ws.Client) *Game {
	player := &Player{
		Profile: s.Lobby[client],
		Team:    1,
	}

	game := CreateGame(player)
	game.CanStartBeforeFull = s.canStartBeforeFull
	s.Games[game.ID] = game

	game.DoWork(
		func(game *Game) {
			reply := CreateMessageView(ViewBoard)
			b, _ := json.Marshal(reply)
			game.Announce(b)
			game.ShareState()
		})

	log.Println(">> Created game ", game.Title)
	return game
}

//JoinGame for easy access
func (s *Server) JoinGame(gameID string, p *Profile) *Game {
	player := &Player{
		Profile: s.Lobby[p.Client],
	}
	game := s.Games[gameID]
	ok := game.JoinGame(player)
	if !ok {
		reply := CreateMessageError("Could not join game", "Server is full")
		b, _ := json.Marshal(reply)
		p.Client.Send <- b
		return game
	}

	reply := CreateMessageView(ViewBoard)
	b, _ := json.Marshal(reply)

	game.DoWork(
		func(game *Game) {
			game.Announce(b)
			game.ShareState()
		})

	log.Println(">> ", player.Profile.Nick, " joined game ", game.Title)
	return game

}

//ListOfGames produces a light struct that describes the games hosted
func (s *Server) ListOfGames() *ListOfGames {
	result := ListOfGames{Games: []map[string]string{}}
	for _, game := range s.Games {
		item := make(map[string]string)
		item["id"] = game.ID
		item["title"] = game.Title
		item["players"] = fmt.Sprint(len(game.Players), "/", game.MaxPlayers())
		result.Games = append(result.Games, item)
	}
	return &result
}

func (s *Server) CreateListOfGames() MessageListOfGames {
	list := s.ListOfGames()
	return CreateMessageListOfGames(list)

}

//SetNick sets profiles nickname
func (s *Server) SetNick(client *ws.Client, nick string) {

	nick = s.createUniqueNick(nick)

	profile := s.GetOrCreateProfile(client)
	profile.Nick = nick

	log.Println(">> Set profile nick: ", profile.Nick)

	reply := CreateMessageSecret(profile.Secret, profile.ID)
	b, _ := json.Marshal(reply)
	client.Send <- b

}

//StartGame starts a game if possible
func (s *Server) StartGame(client *ws.Client) {
	found, game := s.GameByClientOwner(client)
	if !found {
		log.Println(fmt.Sprintf("Error finding game owned by, %v with nick %v", client, s.Lobby[client].Nick))
		return
	}

	game.DoWork(
		func(game *Game) {
			game.StartGame()
		})

}

//Move attempts to move a piece
func (s *Server) Move(message MessageMove, client *ws.Client) {
	foundGame, game := s.GameByClientPlaying(client)
	if !foundGame {
		log.Println(fmt.Sprintf("Error finding game"))
		return
	}

	game.DoWork(
		func(game *Game) {
			game.Move(client, message)
		})

}

//ChangeSeat changes where a player sits
func (s *Server) ChangeSeat(client *ws.Client, seat int) {
	_, game := s.GameByClientPlaying(client)

	game.DoWork(
		func(game *Game) {
			game.ChangeSeat(client, seat)
		})
}

func (s *Server) createUniqueNick(nickIn string) string {
	nick := nickIn
	ok := false
	i := 1
	for !ok {
		ok = true
		for _, b := range s.Lobby {
			if b.Nick == nick {
				ok = false
				break
			}
		}
		if ok {
			break
		}
		i++
		nick = fmt.Sprintf("%s%v", nickIn, i)
	}
	return nick

}

//Disconnect handles changes to server state when someone a websocket disconnects
func (s *Server) Disconnect(client *ws.Client) {
	log.Println(">> Going to handle disconnect")
	found, game := s.GameByClientPlaying(client)
	if found {
		game.RemoveClient(client)
		if len(game.Players) == 0 {
			log.Println(">> Game is empty. Removing game")
			game.Stop()
			delete(s.Games, game.ID)
		}
	} else {
		log.Println(">> Player disconnecting was not in game")
	}

	//Remove from server
	delete(s.Lobby, client)

}

//NotifyLobby tells players without a game about a new game
func (s *Server) NotifyLobby() {
	reply := s.CreateListOfGames()
	b, _ := json.Marshal(&reply)

	for client := range s.Lobby {
		found, _ := s.GameByClientPlaying(client)
		if !found {
			client.Send <- b
		}
	}

}
